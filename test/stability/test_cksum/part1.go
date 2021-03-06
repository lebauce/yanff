// Copyright 2017 Intel Corporation.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/intel-go/yanff/common"
	"github.com/intel-go/yanff/flow"
	"github.com/intel-go/yanff/packet"

	"github.com/intel-go/yanff/test/stability/test_cksum/testCommon"
)

const (
	ipv4  = 0
	ipv5  = 1
	maxL3 = 2
	udp   = 0
	tcp   = 1
	icmp  = 2
	maxL4 = 2
)

var (
	totalPackets uint64 = 10

	// Packet should hold at least two int64 fields
	minPayloadSize = int(unsafe.Sizeof(sentPackets) * 2)
	maxPayloadSize = 1400

	sentPackets     uint64
	receivedPackets uint64
	testDoneEvent   *sync.Cond
	passed          int32 = 1
	rnd             *rand.Rand

	hwol         bool
	inport       uint
	outport      uint
	useIPv4      bool
	useIPv6      bool
	randomL3     = false
	l3type       int
	useUDP       bool
	useTCP       bool
	useICMP      bool
	randomL4     = false
	l4type       int
	packetLength int
)

// This part of test generates packets on port 0 and receives them on
// port 1. The test records packet's index inside of the first field
// of the packet and sets the second field to zero. It expects the
// other half of the test to copy index from first part of the packet
// to the second part. When packet is received, test routine compares
// first and second halves and checks that they are equal. Test also
// calculates sent/received ratio and prints it when a predefined
// number of packets is received.
func main() {
	flag.BoolVar(&hwol, "hwol", false, "Use Hardware offloading for TX checksums calculation")
	flag.UintVar(&inport, "inport", 1, "Input port number")
	flag.UintVar(&outport, "outport", 0, "Output port number")
	flag.BoolVar(&useUDP, "udp", false, "Generate UDP packets")
	flag.BoolVar(&useTCP, "tcp", false, "Generate TCP packets")
	flag.BoolVar(&useICMP, "icmp", false, "Generate ICMP Echo Request packets")
	flag.BoolVar(&useIPv4, "ipv4", false, "Generate IPv4 packets")
	flag.BoolVar(&useIPv6, "ipv6", false, "Generate IPv6 packets")
	flag.IntVar(&packetLength, "size", 0, "Specify length of packets to be generated")
	flag.Uint64Var(&totalPackets, "number", 10, "Number of packets to send")
	flag.Parse()

	rnd = rand.New(rand.NewSource(13))

	// Init YANFF system at 16 available cores
	config := flow.Config{
		CPUCoresNumber: 16,
		HWTXChecksum:   hwol,
	}
	flow.SystemInit(&config)

	if !useIPv4 && !useIPv6 {
		println("No L3 IP mode selected. Enabling IPv4 by default")
		useIPv4 = true
	}

	if useIPv4 && !useIPv6 {
		print("IPv4 L3 and ")
		l3type = ipv4
	} else if !useIPv4 && useIPv6 {
		print("IPv6 L3 and ")
		l3type = ipv5
	} else {
		print("IPv4 and IPv6 L3 and ")
		randomL3 = true
	}

	if !useUDP && !useTCP && !useICMP {
		println("No L4 packet type mode selected. Enabling UDP by default")
		useUDP = true
	}

	if useUDP && !useTCP && !useICMP {
		println("UDP L4 mode is enabled")
		l4type = udp
	} else if !useUDP && useTCP && !useICMP {
		println("TCP L4 mode is enabled")
		l4type = tcp
	} else if !useUDP && !useTCP && useICMP {
		println("ICMP L4 mode is enabled")
		l4type = icmp
	} else {
		println("UDP and TCP L4 modes are enabled")
		randomL4 = true
	}

	if useICMP && hwol {
		println("Cannot use HW offloading with ICMP protocol")
		os.Exit(1)
	}

	var m sync.Mutex
	testDoneEvent = sync.NewCond(&m)

	// Use here usual generator (enabled if speed = 0).
	// High performance generator (enabled if speed != 0) is not used here, as it
	// can send fully only number of packets N which is multiple of burst size (default 32),
	// otherwise last N%burstSize packets are not sent, and cannot send N less than burstSize.
	firstFlow := flow.SetGenerator(generatePacket, 0, nil)
	// Send all generated packets to the output
	flow.SetSender(firstFlow, uint8(outport))

	// Create receiving flow and set a checking function for it
	secondFlow := flow.SetReceiver(uint8(inport))
	flow.SetHandler(secondFlow, checkPackets, nil)
	flow.SetStopper(secondFlow)

	// Start pipeline
	go flow.SystemStart()

	// Wait for enough packets to arrive
	testDoneEvent.L.Lock()
	testDoneEvent.Wait()
	testDoneEvent.L.Unlock()

	// Compose statistics
	sent := atomic.LoadUint64(&sentPackets)
	received := atomic.LoadUint64(&receivedPackets)
	ratio := received * 100 / sent

	// Print report
	println("Sent", sent, "packets")
	println("Received", received, "packets")
	println("Ratio = ", ratio, "%")
	if atomic.LoadInt32(&passed) != 0 {
		println("TEST PASSED")
	} else {
		println("TEST FAILED")
	}
}

func generatePayloadLength() uint16 {
	if packetLength == 0 {
		return uint16(rnd.Intn(maxPayloadSize-minPayloadSize) + minPayloadSize)
	}
	return uint16(packetLength)
}

func generatePacket(emptyPacket *packet.Packet, context flow.UserContext) {
	if randomL3 {
		l3type = rnd.Intn(maxL3)
	}
	if randomL4 {
		l4type = rnd.Intn(maxL4)
	}

	if l3type == ipv4 {
		if l4type == udp {
			generateIPv4UDP(emptyPacket)
		} else if l4type == icmp {
			generateIPv4ICMP(emptyPacket)
		} else {
			generateIPv4TCP(emptyPacket)
		}
	} else {
		if l4type == udp {
			generateIPv6UDP(emptyPacket)
		} else if l4type == icmp {
			generateIPv6ICMP(emptyPacket)
		} else {
			generateIPv6TCP(emptyPacket)
		}
	}

	curValue := atomic.LoadUint64(&sentPackets)

	if curValue >= totalPackets {
		testDoneEvent.Signal()
		// Stop sending packets, let program finish.
		time.Sleep(2 * time.Second)
	}

	atomic.AddUint64(&sentPackets, 1)
}

func initPacketCommon(emptyPacket *packet.Packet, length uint16) {
	// Initialize ethernet addresses
	emptyPacket.Ether.DAddr = [6]uint8{0xde, 0xea, 0xad, 0xbe, 0xee, 0xef}
	emptyPacket.Ether.SAddr = [6]uint8{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}

	// Fill internals with random garbage
	data := (*[1 << 30]byte)(emptyPacket.Data)[0:length]
	for i := range data {
		data[i] = byte(rnd.Int())
	}

	// Put a unique non-zero value here
	sent := atomic.LoadUint64(&sentPackets)
	ptr := (*testCommon.Packetdata)(emptyPacket.Data)
	ptr.F1 = sent + 1
	ptr.F2 = 0
}

func initPacketIPv4(emptyPacket *packet.Packet) {
	// Initialize IPv4 addresses
	emptyPacketIPv4 := emptyPacket.GetIPv4()
	emptyPacketIPv4.SrcAddr = packet.SwapBytesUint32((192 << 24) | (168 << 16) | (1 << 8) | 1)
	emptyPacketIPv4.DstAddr = packet.SwapBytesUint32((192 << 24) | (168 << 16) | (1 << 8) | 2)
	emptyPacketIPv4.TimeToLive = 100
}

func initPacketIPv6(emptyPacket *packet.Packet) {
	// Initialize IPv6 addresses
	emptyPacketIPv6 := emptyPacket.GetIPv6()
	emptyPacketIPv6.SrcAddr = [common.IPv6AddrLen]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	emptyPacketIPv6.DstAddr = [common.IPv6AddrLen]uint8{17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
}

func initPacketUDP(emptyPacket *packet.Packet) {
	emptyPacketUDP := (*packet.UDPHdr)(emptyPacket.L4)
	emptyPacketUDP.SrcPort = packet.SwapBytesUint16(1234)
	emptyPacketUDP.DstPort = packet.SwapBytesUint16(2345)
}

func initPacketTCP(emptyPacket *packet.Packet) {
	emptyPacketTCP := (*packet.TCPHdr)(emptyPacket.L4)
	emptyPacketTCP.SrcPort = packet.SwapBytesUint16(3456)
	emptyPacketTCP.DstPort = packet.SwapBytesUint16(4567)
}

func initPacketICMP(emptyPacket *packet.Packet) {
	emptyPacketICMP := (*packet.ICMPHdr)(emptyPacket.L4)
	emptyPacketICMP.Type = common.ICMPTypeEchoRequest
	emptyPacketICMP.Identifier = 0xdead
	emptyPacketICMP.SeqNum = 0xbeef
}

func generateIPv4UDP(emptyPacket *packet.Packet) {
	length := generatePayloadLength()
	packet.InitEmptyIPv4UDPPacket(emptyPacket, uint(length))

	initPacketCommon(emptyPacket, length)
	initPacketIPv4(emptyPacket)
	initPacketUDP(emptyPacket)

	if hwol {
		emptyPacket.GetUDPForIPv4().DgramCksum = packet.SwapBytesUint16(packet.CalculatePseudoHdrIPv4UDPCksum(emptyPacket.GetIPv4(), emptyPacket.GetUDPForIPv4()))
	} else {
		emptyPacket.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(emptyPacket))
		emptyPacket.GetUDPForIPv4().DgramCksum = packet.SwapBytesUint16(packet.CalculateIPv4UDPChecksum(emptyPacket))
	}
}

func generateIPv4TCP(emptyPacket *packet.Packet) {
	length := generatePayloadLength()
	packet.InitEmptyIPv4TCPPacket(emptyPacket, uint(length))

	initPacketCommon(emptyPacket, length)
	initPacketIPv4(emptyPacket)
	initPacketTCP(emptyPacket)

	if hwol {
		emptyPacket.GetTCPForIPv4().Cksum = packet.SwapBytesUint16(packet.CalculatePseudoHdrIPv4TCPCksum(emptyPacket.GetIPv4()))
	} else {
		emptyPacket.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(emptyPacket))
		emptyPacket.GetTCPForIPv4().Cksum = packet.SwapBytesUint16(packet.CalculateIPv4TCPChecksum(emptyPacket))
	}
}

func generateIPv4ICMP(emptyPacket *packet.Packet) {
	length := generatePayloadLength()
	packet.InitEmptyIPv4ICMPPacket(emptyPacket, uint(length))

	initPacketCommon(emptyPacket, length)
	initPacketIPv4(emptyPacket)
	initPacketICMP(emptyPacket)

	if !hwol {
		emptyPacket.GetIPv4().HdrChecksum = packet.SwapBytesUint16(packet.CalculateIPv4Checksum(emptyPacket))
		emptyPacket.GetICMPForIPv4().Cksum = packet.SwapBytesUint16(packet.CalculateIPv4ICMPChecksum(emptyPacket))
	}
}

func generateIPv6UDP(emptyPacket *packet.Packet) {
	length := generatePayloadLength()
	packet.InitEmptyIPv6UDPPacket(emptyPacket, uint(length))

	initPacketCommon(emptyPacket, length)
	initPacketIPv6(emptyPacket)
	initPacketUDP(emptyPacket)

	if hwol {
		emptyPacket.GetUDPForIPv6().DgramCksum = packet.SwapBytesUint16(packet.CalculatePseudoHdrIPv6UDPCksum(emptyPacket.GetIPv6(), emptyPacket.GetUDPForIPv6()))
	} else {
		emptyPacket.GetUDPForIPv6().DgramCksum = packet.SwapBytesUint16(packet.CalculateIPv6UDPChecksum(emptyPacket))
	}
}

func generateIPv6TCP(emptyPacket *packet.Packet) {
	length := generatePayloadLength()
	packet.InitEmptyIPv6TCPPacket(emptyPacket, uint(length))

	initPacketCommon(emptyPacket, length)
	initPacketIPv6(emptyPacket)
	initPacketTCP(emptyPacket)

	if hwol {
		emptyPacket.GetTCPForIPv6().Cksum = packet.SwapBytesUint16(packet.CalculatePseudoHdrIPv6TCPCksum(emptyPacket.GetIPv6()))
	} else {
		emptyPacket.GetTCPForIPv6().Cksum = packet.SwapBytesUint16(packet.CalculateIPv6TCPChecksum(emptyPacket))
	}
}

func generateIPv6ICMP(emptyPacket *packet.Packet) {
	length := generatePayloadLength()
	packet.InitEmptyIPv6ICMPPacket(emptyPacket, uint(length))

	initPacketCommon(emptyPacket, length)
	initPacketIPv6(emptyPacket)
	initPacketICMP(emptyPacket)

	if !hwol {
		emptyPacket.GetICMPForIPv6().Cksum = packet.SwapBytesUint16(packet.CalculateIPv6ICMPChecksum(emptyPacket))
	}
}

func checkPackets(pkt *packet.Packet, context flow.UserContext) {
	offset := pkt.ParseData()

	if !testCommon.CheckPacketChecksums(pkt) {
		println("TEST FAILED")
	}

	newValue := atomic.AddUint64(&receivedPackets, 1)

	if offset < 0 {
		println("ParseL4 returned negative value", offset)
		println("TEST FAILED")
		atomic.StoreInt32(&passed, 0)
	} else {
		ptr := (*testCommon.Packetdata)(pkt.Data)

		if ptr.F1 != ptr.F2 {
			fmt.Printf("Data mismatch in the packet, read %x and %x\n", ptr.F1, ptr.F2)
			println("TEST FAILED")
			atomic.StoreInt32(&passed, 0)
		} else if ptr.F1 == 0 {
			println("Zero data value encountered in the packet")
			println("TEST FAILED")
			atomic.StoreInt32(&passed, 0)
		}
	}

	if newValue >= totalPackets {
		testDoneEvent.Signal()
	}
}
