// Copyright 2017 Intel Corporation.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/intel-go/yanff/common"
	"github.com/intel-go/yanff/flow"
	"github.com/intel-go/yanff/packet"
	"github.com/intel-go/yanff/test/localTesting/pktgen/parseConfig"
)

var (
	count uint64

	testDoneEvent *sync.Cond
	configuration *parseConfig.PacketConfig

	outFile      string
	inFile       string
	totalPackets uint64
)

func main() {
	flag.StringVar(&outFile, "outfile", "pkts_generated.pcap", "file to write output to")
	flag.StringVar(&inFile, "infile", "config.json", "file with configurations for generator")
	flag.Uint64Var(&totalPackets, "totalPackets", 10000000, "stop after generation totalPackets number")
	flag.Parse()

	var err error
	configuration, err = ReadConfig(inFile)
	if err != nil {
		panic(fmt.Sprintf("config reading failed: %v", err))
	}

	// Init YANFF system at 16 available cores
	config := flow.Config{
		CPUCoresNumber: 16,
	}
	flow.SystemInit(&config)

	var m sync.Mutex
	testDoneEvent = sync.NewCond(&m)

	generator, err := getGenerator()
	if err != nil {
		panic(fmt.Sprintf("determining generator type failed: %v", err))
	}
	// Create packet flow
	outputFlow := flow.SetGenerator(generator, 0, nil)
	flow.SetWriter(outputFlow, outFile)
	// Start pipeline
	go flow.SystemStart()

	// Wait for enough packets to arrive
	testDoneEvent.L.Lock()
	testDoneEvent.Wait()
	testDoneEvent.L.Unlock()

	sent := atomic.LoadUint64(&count)

	// Print report
	println("Sent", sent, "packets")
}

// ReadConfig function reads and parses config file.
func ReadConfig(fileName string) (*parseConfig.PacketConfig, error) {
	f, err := os.Open(fileName)
	if err != nil {
		return nil, fmt.Errorf("opening file failed with: %v ", err)
	}
	cfg, err := parseConfig.ParsePacketConfig(f)
	if err != nil {
		return nil, fmt.Errorf("parsing config failed with: %v", err)
	}
	return cfg, nil
}

func getNextAddr(addr parseConfig.AddrRange) (ret []uint8) {
	if len(addr.Start) == 0 {
		return []uint8{0}
	}
	if bytes.Compare(addr.Start, addr.Min) > 0 || bytes.Compare(addr.Start, addr.Max) < 0 {
		addr.Start = addr.Min
	}
	ret = addr.Start
	IPv6Int := big.NewInt(0)
	IPv6Int.SetBytes(addr.Start)
	IPv6Int.Add(big.NewInt(int64(addr.Incr)), IPv6Int)
	newAddr := IPv6Int.Bytes()
	if len(newAddr) >= len(addr.Start) {
		copy(addr.Start[:], newAddr[len(newAddr)-len(addr.Start):])
	} else {
		copy(addr.Start[len(addr.Start)-len(newAddr):], newAddr[:])
	}
	if bytes.Compare(addr.Start, addr.Max) <= 0 {
		addr.Start = addr.Min
	}
	return ret
}

func getNextPort(port parseConfig.PortRange) uint16 {
	if len(port.Start) == 0 {
		return 0
	}
	if port.Start[0] < port.Min || port.Start[0] > port.Max {
		port.Start[0] = port.Min
	}
	port.Start[0] += port.Incr
	if port.Start[0] > port.Max {
		port.Start[0] = port.Min
	}
	return port.Start[0]
}

func getNextSeqNumber(seq parseConfig.Sequence) (ret uint32) {
	if len(seq.Next) == 0 {
		return 0
	}
	ret = seq.Next[0]
	if seq.Type == parseConfig.RANDOM {
		seq.Next[0] = rand.Uint32()
	} else if seq.Type == parseConfig.INCREASING {
		seq.Next[0]++
	}
	return ret
}

func generateData(configuration interface{}) ([]uint8, error) {
	switch data := configuration.(type) {
	case parseConfig.Raw:
		pktData := make([]uint8, len(data.Data))
		copy(pktData[:], ([]uint8(data.Data)))
		return pktData, nil
	case parseConfig.RandBytes:
		maxZise := data.Size + data.Deviation
		minSize := data.Size - data.Deviation
		randSize := uint(rand.Float64()*float64(maxZise-minSize) + float64(minSize))
		pktData := make([]uint8, randSize)
		for i := range pktData {
			pktData[i] = byte(rand.Int())
		}
		return pktData, nil
	case []parseConfig.PDistEntry:
		prob := 0.0
		maxProb := parseConfig.PDistEntry{Probability: 0}
		for _, item := range data {
			prob += item.Probability
			if item.Probability > maxProb.Probability {
				maxProb = item
			}
		}
		if prob <= 0 || prob > 1 {
			return nil, fmt.Errorf("sum of pdist probabilities is invalid, %f", prob)
		}
		rndN := math.Abs(rand.Float64())
		prob = 0.0
		for _, item := range data {
			prob += item.Probability
			if rndN <= prob {
				pktData, err := generateData(item.Data)
				if err != nil {
					return nil, fmt.Errorf("failed to fill data with pdist data type: %v", err)
				}
				return pktData, nil
			}
		}
		// get the variant with max prob
		// if something went wrong and rand did not match any prob
		// may happen if sum of prob was not 1
		pktData, err := generateData(maxProb.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to fill data with pdist data type: %v", err)
		}
		return pktData, nil
	}
	return nil, nil
}

func getGenerator() (interface{}, error) {
	switch l2 := (configuration.Data).(type) {
	case parseConfig.EtherConfig:
		switch l3 := l2.Data.(type) {
		case parseConfig.IPConfig:
			switch l3.Data.(type) {
			case parseConfig.TCPConfig:
				return generateTCPIP, nil
			case parseConfig.UDPConfig:
				return generateUDPIP, nil
			case parseConfig.ICMPConfig:
				return generateICMPIP, nil
			case parseConfig.Raw, parseConfig.RandBytes, []parseConfig.PDistEntry:
				return generateIP, nil
			default:
				return nil, fmt.Errorf("unknown packet l4 configuration")
			}
		case parseConfig.Raw, parseConfig.RandBytes, []parseConfig.PDistEntry:
			return generateEther, nil
		default:
			return nil, fmt.Errorf("unknown packet l3 configuration")
		}
	default:
		return nil, fmt.Errorf("unknown packet l2 configuration")
	}
}

func checkFinish() {
	if count >= totalPackets {
		time.Sleep(time.Second * 3)
		testDoneEvent.Signal()
	}
	atomic.AddUint64(&count, 1)
}

func generateEther(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	checkFinish()
	l2 := configuration.Data.(parseConfig.EtherConfig)
	data, err := generateData(l2.Data)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse data for l2: %v", err))
	}
	if data != nil {
		size := uint(len(data))
		if !packet.InitEmptyPacket(pkt, size) {
			panic(fmt.Sprintf("InitEmptyPacket returned false"))
		}
		copy((*[1 << 30]uint8)(pkt.Data)[0:size], data)
	} else {
		panic(fmt.Sprintf("failed to generate data"))
	}
	if err := fillEtherHdr(pkt, l2); err != nil {
		panic(fmt.Sprintf("failed to fill ether header %v", err))
	}
}

func generateIP(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	checkFinish()
	l2 := configuration.Data.(parseConfig.EtherConfig)
	l3 := l2.Data.(parseConfig.IPConfig)
	data, err := generateData(l3.Data)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse data for l3: %v", err))
	}
	if data != nil {
		size := uint(len(data))
		if l3.Version == 4 {
			if !packet.InitEmptyIPv4Packet(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv4Packet returned false"))
			}
		} else if l3.Version == 6 {
			if !packet.InitEmptyIPv6Packet(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv6Packet returned false"))
			}
		} else {
			panic(fmt.Sprintf("fillPacketl3 failed, unknovn version %d", l3.Version))
		}
		copy((*[1 << 30]uint8)(pkt.Data)[0:size], data)
	} else {
		panic(fmt.Sprintf("failed to generate data"))
	}
	if err := fillIPHdr(pkt, l3); err != nil {
		panic(fmt.Sprintf("failed to fill ip header %v", err))
	}
	if err := fillEtherHdr(pkt, l2); err != nil {
		panic(fmt.Sprintf("failed to fill ether header %v", err))
	}
	if l3.Version == 4 {
		pkt.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(pkt))
	}
}

func generateTCPIP(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	checkFinish()
	l2 := configuration.Data.(parseConfig.EtherConfig)
	l3 := l2.Data.(parseConfig.IPConfig)
	l4 := l3.Data.(parseConfig.TCPConfig)
	data, err := generateData(l4.Data)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse data for l4: %v", err))
	}
	if data != nil {
		size := uint(len(data))
		if l3.Version == 4 {
			if !packet.InitEmptyIPv4TCPPacket(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv4TCPPacket returned false"))
			}
		} else if l3.Version == 6 {
			if !packet.InitEmptyIPv6TCPPacket(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv6TCPPacket returned false"))
			}
		} else {
			panic(fmt.Sprintf("fill packet l4 failed, unknovn version %d", l3.Version))
		}
		copy((*[1 << 30]uint8)(pkt.Data)[0:size], data)
	} else {
		panic(fmt.Sprintf("failed to generate data"))
	}
	if err := fillTCPHdr(pkt, l4); err != nil {
		panic(fmt.Sprintf("failed to fill tcp header %v", err))
	}
	if err := fillIPHdr(pkt, l3); err != nil {
		panic(fmt.Sprintf("failed to fill ip header %v", err))
	}
	if err := fillEtherHdr(pkt, l2); err != nil {
		panic(fmt.Sprintf("failed to fill ether header %v", err))
	}
	if l3.Version == 4 {
		pkt.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(pkt))
		pkt.GetTCPForIPv4().Cksum = packet.SwapBytesUint16(packet.CalculateIPv4TCPChecksum(pkt))
	} else if l3.Version == 6 {
		pkt.GetTCPForIPv6().Cksum = packet.SwapBytesUint16(packet.CalculateIPv6TCPChecksum(pkt))
	}
}

func generateUDPIP(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	checkFinish()
	l2 := configuration.Data.(parseConfig.EtherConfig)
	l3 := l2.Data.(parseConfig.IPConfig)
	l4 := l3.Data.(parseConfig.UDPConfig)
	data, err := generateData(l4.Data)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse data for l4: %v", err))
	}
	if data != nil {
		size := uint(len(data))
		if l3.Version == 4 {
			if !packet.InitEmptyIPv4UDPPacket(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv4UDPPacket returned false"))
			}
		} else if l3.Version == 6 {
			if !packet.InitEmptyIPv6UDPPacket(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv6UDPPacket returned false"))
			}
		} else {
			panic(fmt.Sprintf("fill packet l4 failed, unknovn version %d", l3.Version))
		}
		copy((*[1 << 30]uint8)(pkt.Data)[0:size], data)
	} else {
		panic(fmt.Sprintf("failed to generate data"))
	}
	if err := fillUDPHdr(pkt, l4); err != nil {
		panic(fmt.Sprintf("failed to fill udp header %v", err))
	}
	if err := fillIPHdr(pkt, l3); err != nil {
		panic(fmt.Sprintf("failed to fill ip header %v", err))
	}
	if err := fillEtherHdr(pkt, l2); err != nil {
		panic(fmt.Sprintf("failed to fill ether header %v", err))
	}
	if l3.Version == 4 {
		pkt.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(pkt))
		pkt.GetUDPForIPv4().DgramCksum = packet.SwapBytesUint16(packet.CalculateIPv4UDPChecksum(pkt))
	} else if l3.Version == 6 {
		pkt.GetUDPForIPv6().DgramCksum = packet.SwapBytesUint16(packet.CalculateIPv6UDPChecksum(pkt))
	}
}

func generateICMPIP(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	checkFinish()
	l2 := configuration.Data.(parseConfig.EtherConfig)
	l3 := l2.Data.(parseConfig.IPConfig)
	l4 := l3.Data.(parseConfig.ICMPConfig)
	data, err := generateData(l4.Data)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse data for l4: %v", err))
	}
	if data != nil {
		size := uint(len(data))
		if l3.Version == 4 {
			if !packet.InitEmptyIPv4ICMPPacket(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv4ICMPPacket returned false"))
			}
		} else if l3.Version == 6 {
			if !packet.InitEmptyIPv6ICMPPacket(pkt, size) {
				panic(fmt.Sprintf("InitEmptyIPv6ICMPPacket returned false"))
			}
		} else {
			panic(fmt.Sprintf("fill packet l4 failed, unknovn version %d", l3.Version))
		}
		copy((*[1 << 30]uint8)(pkt.Data)[0:size], data)
	} else {
		panic(fmt.Sprintf("failed to generate data"))
	}
	if err := fillICMPHdr(pkt, l4); err != nil {
		panic(fmt.Sprintf("failed to fill icmp header %v", err))
	}
	if err := fillIPHdr(pkt, l3); err != nil {
		panic(fmt.Sprintf("failed to fill ip header %v", err))
	}
	if err := fillEtherHdr(pkt, l2); err != nil {
		panic(fmt.Sprintf("failed to fill ether header %v", err))
	}
	if l3.Version == 4 {
		pkt.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(pkt))
		pkt.GetICMPForIPv4().Cksum = packet.SwapBytesUint16(packet.CalculateIPv4ICMPChecksum(pkt))
	} else if l3.Version == 6 {
		pkt.GetICMPForIPv6().Cksum = packet.SwapBytesUint16(packet.CalculateIPv6ICMPChecksum(pkt))
	}
}

func fillTCPHdr(pkt *packet.Packet, l4 parseConfig.TCPConfig) error {
	emptyPacketTCP := (*packet.TCPHdr)(pkt.L4)
	emptyPacketTCP.SrcPort = packet.SwapBytesUint16(getNextPort(l4.SPort))
	emptyPacketTCP.DstPort = packet.SwapBytesUint16(getNextPort(l4.DPort))
	emptyPacketTCP.SentSeq = packet.SwapBytesUint32(getNextSeqNumber(l4.Seq))
	emptyPacketTCP.TCPFlags = l4.Flags
	return nil
}

func fillUDPHdr(pkt *packet.Packet, l4 parseConfig.UDPConfig) error {
	emptyPacketUDP := (*packet.UDPHdr)(pkt.L4)
	emptyPacketUDP.SrcPort = packet.SwapBytesUint16(getNextPort(l4.SPort))
	emptyPacketUDP.DstPort = packet.SwapBytesUint16(getNextPort(l4.DPort))
	return nil
}

func fillICMPHdr(pkt *packet.Packet, l4 parseConfig.ICMPConfig) error {
	emptyPacketICMP := (*packet.ICMPHdr)(pkt.L4)
	// TODO: why segfault ??
	emptyPacketICMP.Type = l4.Type
	emptyPacketICMP.Code = l4.Code
	emptyPacketICMP.Identifier = l4.Identifier
	emptyPacketICMP.SeqNum = packet.SwapBytesUint16(uint16(getNextSeqNumber(l4.Seq)))
	return nil
}

func fillIPHdr(pkt *packet.Packet, l3 parseConfig.IPConfig) error {
	if l3.Version == 4 {
		pktIP := pkt.GetIPv4()
		pktIP.SrcAddr = binary.LittleEndian.Uint32(net.IP(getNextAddr(l3.SAddr)).To4())
		pktIP.DstAddr = binary.LittleEndian.Uint32(net.IP(getNextAddr(l3.DAddr)).To4())
		return nil
	}
	pktIP := pkt.GetIPv6()
	nextAddr := getNextAddr(l3.SAddr)
	copy(pktIP.SrcAddr[:], nextAddr[len(nextAddr)-common.IPv6AddrLen:])
	nextAddr = getNextAddr(l3.DAddr)
	copy(pktIP.DstAddr[:], nextAddr[len(nextAddr)-common.IPv6AddrLen:])
	return nil
}

func fillEtherHdr(pkt *packet.Packet, l2 parseConfig.EtherConfig) error {
	nextAddr := getNextAddr(l2.DAddr)
	copy(pkt.Ether.DAddr[:], nextAddr[len(nextAddr)-common.EtherAddrLen:])
	nextAddr = getNextAddr(l2.SAddr)
	copy(pkt.Ether.SAddr[:], nextAddr[len(nextAddr)-common.EtherAddrLen:])
	return nil
}
