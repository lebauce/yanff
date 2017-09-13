// Copyright 2017 Intel Corporation.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/md5"
	"flag"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/intel-go/yanff/flow"
	"github.com/intel-go/yanff/packet"
)

// test-handle2-part1: sends packets to 0 port, receives from 0 and 1 ports.
// This part of test generates three packet flows (1st, 2nd and 3rd), merges them into one flow
// and send it to 0 port. Packets in original 1st, 2nd and 3rd flows has UDP destination addresses
// dstPort1, dstPort2, dstPort3 respectively. For each packet sender calculates md5 hash sum
// from all headers, write it to packet.Data and check it on packet receive.
// This part of test receive packets on 0 port, expects to receive ~33% of packets.
// Test also calculates number of broken packets and prints it when a predefined number
// of packets is received.
//
// test-handle2-part2:
// This part of test receives packets on 0 port, separate input flow according to rules
// in test-separate-l3rules.conf into 2 flows. Accepted flow sent to 0 port, rejected flow is stopped.

const (
	totalPackets = 100000000

	// Test expects to receive 33% of packets on 0 port.
	// Test is PASSSED, if p1 is in [low1;high1]
	eps   = 5
	high1 = 33 + eps
	low1  = 33 - eps
)

var (
	// Payload is 16 byte md5 hash sum of headers
	payloadSize uint = 16
	d           uint = 10

	sentPacketsGroup1 uint64
	sentPacketsGroup2 uint64
	sentPacketsGroup3 uint64

	recv          uint64
	brokenPackets uint64

	dstPort1 uint16 = 111
	dstPort2 uint16 = 222
	dstPort3 uint16 = 333

	testDoneEvent *sync.Cond

	outport uint
	inport  uint
)

func main() {
	flag.UintVar(&outport, "outport", 0, "port for sender")
	flag.UintVar(&inport, "inport", 0, "port for receiver")
	flag.Parse()

	// Init YANFF system at 16 available cores.
	config := flow.Config{
		CPUCoresNumber: 16,
	}
	flow.SystemInit(&config)

	var m sync.Mutex
	testDoneEvent = sync.NewCond(&m)

	// Create first packet flow
	flow1 := flow.SetGenerator(generatePacketGroup1, 0, nil)
	flow2 := flow.SetGenerator(generatePacketGroup2, 0, nil)
	flow3 := flow.SetGenerator(generatePacketGroup3, 0, nil)

	outputFlow := flow.SetMerger(flow1, flow2, flow3)

	flow.SetSender(outputFlow, uint8(outport))

	// Create receiving flows and set a checking function for it
	inputFlow1 := flow.SetReceiver(uint8(outport))
	flow.SetHandler(inputFlow1, checkInputFlow, nil)
	flow.SetStopper(inputFlow1)

	// Start pipeline
	go flow.SystemStart()

	// Wait for enough packets to arrive
	testDoneEvent.L.Lock()
	testDoneEvent.Wait()
	testDoneEvent.L.Unlock()

	// Compose statistics
	sent1 := sentPacketsGroup1
	sent2 := sentPacketsGroup2
	sent3 := sentPacketsGroup3
	sent := sent1 + sent2 + sent3

	received := atomic.LoadUint64(&recv)

	var p int
	if sent != 0 {
		p = int(received * 100 / sent)
	}
	broken := atomic.LoadUint64(&brokenPackets)

	// Print report
	println("Sent", sent, "packets")
	println("Received", received, "packets")
	println("Proportion of received packets ", p, "%")
	println("Broken = ", broken, "packets")

	// Test is PASSSED, if p is ~33%
	if p <= high1 && p >= low1 {
		println("TEST PASSED")
	} else {
		println("TEST FAILED")
	}

}

// Generate packets of 1 group
func generatePacketGroup1(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	if packet.InitEmptyIPv4UDPPacket(pkt, payloadSize) == false {
		panic("Failed to init empty packet")
	}
	pkt.GetUDPForIPv4().DstPort = packet.SwapBytesUint16(dstPort1)

	// Extract headers of packet
	headerSize := uintptr(pkt.Data) - pkt.Start()
	hdrs := (*[1000]byte)(unsafe.Pointer(pkt.Start()))[0:headerSize]
	ptr := (*packetData)(pkt.Data)
	ptr.HdrsMD5 = md5.Sum(hdrs)

	sentPacketsGroup1++
	if sentPacketsGroup1 > totalPackets/3 {
		time.Sleep(time.Second * time.Duration(d))
		println("TEST FAILED")
	}
}

// Generate packets of 2 group
func generatePacketGroup2(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	if packet.InitEmptyIPv4UDPPacket(pkt, payloadSize) == false {
		panic("Failed to init empty packet")
	}
	pkt.GetUDPForIPv4().DstPort = packet.SwapBytesUint16(dstPort2)

	// Extract headers of packet
	headerSize := uintptr(pkt.Data) - pkt.Start()
	hdrs := (*[1000]byte)(unsafe.Pointer(pkt.Start()))[0:headerSize]
	ptr := (*packetData)(pkt.Data)
	ptr.HdrsMD5 = md5.Sum(hdrs)

	sentPacketsGroup2++
	if sentPacketsGroup1 > totalPackets/3 {
		time.Sleep(time.Second * time.Duration(d))
		println("TEST FAILED")
	}
}

// Generate packets of 3 group
func generatePacketGroup3(pkt *packet.Packet, context flow.UserContext) {
	if pkt == nil {
		panic("Failed to create new packet")
	}
	if packet.InitEmptyIPv4UDPPacket(pkt, payloadSize) == false {
		panic("Failed to init empty packet")
	}
	pkt.GetUDPForIPv4().DstPort = packet.SwapBytesUint16(dstPort3)

	// Extract headers of packet
	headerSize := uintptr(pkt.Data) - pkt.Start()
	hdrs := (*[1000]byte)(unsafe.Pointer(pkt.Start()))[0:headerSize]
	ptr := (*packetData)(pkt.Data)
	ptr.HdrsMD5 = md5.Sum(hdrs)

	sentPacketsGroup3++
	if sentPacketsGroup1 > totalPackets/3 {
		time.Sleep(time.Second * time.Duration(d))
		println("TEST FAILED")
	}
}

func checkInputFlow(pkt *packet.Packet, context flow.UserContext) {
	offset := pkt.ParseData()
	if offset < 0 {
		println("ParseData returned negative value", offset)
		// Some received packets are not generated by this example
		// They cannot be parsed due to unknown protocols, skip them
	} else {
		ptr := (*packetData)(pkt.Data)

		// Recompute hash to check how many packets are valid
		headerSize := uintptr(pkt.Data) - pkt.Start()
		hdrs := (*[1000]byte)(unsafe.Pointer(pkt.Start()))[0:headerSize]
		hash := md5.Sum(hdrs)

		if hash != ptr.HdrsMD5 {
			// Packet is broken
			atomic.AddUint64(&brokenPackets, 1)
			return
		}
		atomic.AddUint64(&recv, 1)
	}
	// TODO 80% of requested number of packets.
	if recv >= totalPackets/32*8 {
		testDoneEvent.Signal()
	}
}

type packetData struct {
	HdrsMD5 [16]byte
}
