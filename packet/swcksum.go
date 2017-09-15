// Copyright 2017 Intel Corporation.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package packet

import (
	. "github.com/intel-go/yanff/common"
	"unsafe"
)

// Calculates checksum of memory for a given pointer. Length and
// offset are in bytes. Offset is signed, so negative offset is
// possible. Checksum is calculated in uint16 words. Returned is
// checksum with carry, so carry should be added and value negated for
// use as network checksum.
func calculateDataChecksum(ptr unsafe.Pointer, length, offset int) uint32 {
	var sum uint32
	uptr := uintptr(ptr) + uintptr(offset)

	slice := (*[1 << 30]uint16)(unsafe.Pointer(uptr))[0 : length/2]
	for i := range slice {
		sum += uint32(SwapBytesUint16(slice[i]))
	}

	if length&1 != 0 {
		sum += uint32(*(*byte)(unsafe.Pointer(uptr + uintptr(length-1)))) << 8
	}

	return sum
}

// CalculatePseudoHdrIPv4TCPCksum implements one step of TCP checksum calculation. Separately computes checksum
// for TCP pseudo-header for case if L3 protocol is IPv4.
// This precalculation is required for checksum compute by hardware offload.
// Result should be put into TCP.Cksum field. See test_cksum as an example.
func CalculatePseudoHdrIPv4TCPCksum(hdr *IPv4Hdr) uint16 {
	dataLength := SwapBytesUint16(hdr.TotalLength) - IPv4MinLen
	pHdrCksum := calculateIPv4AddrChecksum(hdr) +
		uint32(hdr.NextProtoID) +
		uint32(dataLength)
	return reduceChecksum(pHdrCksum)
}

// CalculatePseudoHdrIPv4UDPCksum implements one step of UDP checksum calculation. Separately computes checksum
// for TCP pseudo-header for case if L3 protocol is IPv4.
// This precalculation is required for checksum compute by hardware offload.
// Result should be put into UDP.DgramCksum field. See test_cksum as an example.
func CalculatePseudoHdrIPv4UDPCksum(hdr *IPv4Hdr, udp *UDPHdr) uint16 {
	pHdrCksum := calculateIPv4AddrChecksum(hdr) +
		uint32(hdr.NextProtoID) +
		uint32(SwapBytesUint16(udp.DgramLen))
	return reduceChecksum(pHdrCksum)
}

// CalculatePseudoHdrIPv6TCPCksum implements one step of TCP checksum calculation. Separately computes checksum
// for TCP pseudo-header for case if L3 protocol is IPv6.
// This precalculation is required for checksum compute by hardware offload.
// Result should be put into TCP.Cksum field. See test_cksum as an example.
func CalculatePseudoHdrIPv6TCPCksum(hdr *IPv6Hdr) uint16 {
	dataLength := SwapBytesUint16(hdr.PayloadLen)
	pHdrCksum := calculateIPv6AddrChecksum(hdr) +
		uint32(dataLength) +
		uint32(hdr.Proto)
	return reduceChecksum(pHdrCksum)
}

// CalculatePseudoHdrIPv6UDPCksum implements one step of UDP checksum calculation. Separately computes checksum
// for UDP pseudo-header for case if L3 protocol is IPv6.
// This precalculation is required for checksum compute by hardware offload.
// Result should be put into UDP.DgramCksum field. See test_cksum as an example.
func CalculatePseudoHdrIPv6UDPCksum(hdr *IPv6Hdr, udp *UDPHdr) uint16 {
	pHdrCksum := calculateIPv6AddrChecksum(hdr) +
		uint32(hdr.Proto) +
		uint32(SwapBytesUint16(udp.DgramLen))
	return reduceChecksum(pHdrCksum)
}

// SetPseudoHdrChecksum makes precalculation of pseudo header checksum. Separately computes
// checksum for required pseudo-header and writes result to correct place. This
// is required for checksum compute by hardware offload.
func SetPseudoHdrChecksum(p *Packet) {
	ipv4, ipv6 := p.ParseAllKnownL3()
	if ipv4 != nil {
		p.GetIPv4().HdrChecksum = 0
		tcp, udp, _ := p.ParseAllKnownL4ForIPv4()
		if tcp != nil {
			p.GetTCPForIPv4().Cksum = SwapBytesUint16(CalculatePseudoHdrIPv4TCPCksum(p.GetIPv4()))
		} else if udp != nil {
			p.GetUDPForIPv4().DgramCksum = SwapBytesUint16(CalculatePseudoHdrIPv4UDPCksum(p.GetIPv4(), p.GetUDPForIPv4()))
		}
	} else if ipv6 != nil {
		tcp, udp, _ := p.ParseAllKnownL4ForIPv6()
		if tcp != nil {
			p.GetTCPForIPv6().Cksum = SwapBytesUint16(CalculatePseudoHdrIPv6TCPCksum(p.GetIPv6()))
		} else if udp != nil {
			p.GetUDPForIPv6().DgramCksum = SwapBytesUint16(CalculatePseudoHdrIPv6UDPCksum(p.GetIPv6(), p.GetUDPForIPv6()))
		}
	}
}

func reduceChecksum(sum uint32) uint16 {
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return uint16(sum)
}

// CalculateIPv4Checksum calculates checksum of IP header
func CalculateIPv4Checksum(p *Packet) uint16 {
	var sum uint32
	hdr := p.GetIPv4()
	sum = uint32(hdr.VersionIhl)<<8 + uint32(hdr.TypeOfService) +
		uint32(SwapBytesUint16(hdr.TotalLength)) +
		uint32(SwapBytesUint16(hdr.PacketID)) +
		uint32(SwapBytesUint16(hdr.FragmentOffset)) +
		uint32(hdr.TimeToLive)<<8 + uint32(hdr.NextProtoID) +
		uint32(SwapBytesUint16(uint16(hdr.SrcAddr>>16))) +
		uint32(SwapBytesUint16(uint16(hdr.SrcAddr))) +
		uint32(SwapBytesUint16(uint16(hdr.DstAddr>>16))) +
		uint32(SwapBytesUint16(uint16(hdr.DstAddr)))

	return ^reduceChecksum(sum)
}

func calculateIPv4AddrChecksum(hdr *IPv4Hdr) uint32 {
	return uint32(SwapBytesUint16(uint16(hdr.SrcAddr>>16))) +
		uint32(SwapBytesUint16(uint16(hdr.SrcAddr))) +
		uint32(SwapBytesUint16(uint16(hdr.DstAddr>>16))) +
		uint32(SwapBytesUint16(uint16(hdr.DstAddr)))
}

// CalculateIPv4UDPChecksum calculates UDP checksum for case if L3 protocol is IPv4.
func CalculateIPv4UDPChecksum(p *Packet) uint16 {
	hdr := p.GetIPv4()
	udp := p.GetUDPForIPv4()
	dataLength := SwapBytesUint16(hdr.TotalLength) - IPv4MinLen

	sum := calculateDataChecksum(p.Data, int(dataLength-UDPLen), 0)

	sum += calculateIPv4AddrChecksum(hdr) +
		uint32(hdr.NextProtoID) +
		uint32(SwapBytesUint16(udp.DgramLen)) +
		uint32(SwapBytesUint16(udp.SrcPort)) +
		uint32(SwapBytesUint16(udp.DstPort)) +
		uint32(SwapBytesUint16(udp.DgramLen))

	return ^reduceChecksum(sum)
}

func calculateTCPChecksum(tcp *TCPHdr) uint32 {
	return uint32(SwapBytesUint16(tcp.SrcPort)) +
		uint32(SwapBytesUint16(tcp.DstPort)) +
		uint32(SwapBytesUint16(uint16(tcp.SentSeq>>16))) +
		uint32(SwapBytesUint16(uint16(tcp.SentSeq))) +
		uint32(SwapBytesUint16(uint16(tcp.RecvAck>>16))) +
		uint32(SwapBytesUint16(uint16(tcp.RecvAck))) +
		uint32(tcp.DataOff)<<8 +
		uint32(tcp.TCPFlags) +
		uint32(SwapBytesUint16(tcp.RxWin)) +
		uint32(SwapBytesUint16(tcp.TCPUrp))
}

// CalculateIPv4TCPChecksum calculates TCP checksum for case if L3 protocol is IPv4.
func CalculateIPv4TCPChecksum(p *Packet) uint16 {
	hdr := p.GetIPv4()
	tcp := p.GetTCPForIPv4()

	dataLength := SwapBytesUint16(hdr.TotalLength) - IPv4MinLen

	sum := calculateDataChecksum(p.Data, int(dataLength-TCPMinLen), 0)

	sum += calculateIPv4AddrChecksum(hdr) +
		uint32(hdr.NextProtoID) +
		uint32(dataLength) +
		calculateTCPChecksum(tcp)

	return ^reduceChecksum(sum)
}

func calculateIPv6AddrChecksum(hdr *IPv6Hdr) uint32 {
	return uint32(uint16(hdr.SrcAddr[0])<<8|uint16(hdr.SrcAddr[1])) +
		uint32(uint16(hdr.SrcAddr[2])<<8|uint16(hdr.SrcAddr[3])) +
		uint32(uint16(hdr.SrcAddr[4])<<8|uint16(hdr.SrcAddr[5])) +
		uint32(uint16(hdr.SrcAddr[6])<<8|uint16(hdr.SrcAddr[7])) +
		uint32(uint16(hdr.SrcAddr[8])<<8|uint16(hdr.SrcAddr[9])) +
		uint32(uint16(hdr.SrcAddr[10])<<8|uint16(hdr.SrcAddr[11])) +
		uint32(uint16(hdr.SrcAddr[12])<<8|uint16(hdr.SrcAddr[13])) +
		uint32(uint16(hdr.SrcAddr[14])<<8|uint16(hdr.SrcAddr[15])) +
		uint32(uint16(hdr.DstAddr[0])<<8|uint16(hdr.DstAddr[1])) +
		uint32(uint16(hdr.DstAddr[2])<<8|uint16(hdr.DstAddr[3])) +
		uint32(uint16(hdr.DstAddr[4])<<8|uint16(hdr.DstAddr[5])) +
		uint32(uint16(hdr.DstAddr[6])<<8|uint16(hdr.DstAddr[7])) +
		uint32(uint16(hdr.DstAddr[8])<<8|uint16(hdr.DstAddr[9])) +
		uint32(uint16(hdr.DstAddr[10])<<8|uint16(hdr.DstAddr[11])) +
		uint32(uint16(hdr.DstAddr[12])<<8|uint16(hdr.DstAddr[13])) +
		uint32(uint16(hdr.DstAddr[14])<<8|uint16(hdr.DstAddr[15]))
}

// CalculateIPv6UDPChecksum calculates UDP checksum for case if L3 protocol is IPv6.
func CalculateIPv6UDPChecksum(p *Packet) uint16 {
	hdr := p.GetIPv6()
	udp := p.GetUDPForIPv6()
	dataLength := SwapBytesUint16(hdr.PayloadLen)

	sum := calculateDataChecksum(p.Data, int(dataLength-UDPLen), 0)

	sum += calculateIPv6AddrChecksum(hdr) +
		uint32(SwapBytesUint16(udp.DgramLen)) +
		uint32(hdr.Proto) +
		uint32(SwapBytesUint16(udp.SrcPort)) +
		uint32(SwapBytesUint16(udp.DstPort)) +
		uint32(SwapBytesUint16(udp.DgramLen))

	return ^reduceChecksum(sum)
}

// CalculateIPv6TCPChecksum calculates TCP checksum for case if L3 protocol is IPv6.
func CalculateIPv6TCPChecksum(p *Packet) uint16 {
	hdr := p.GetIPv6()
	tcp := p.GetTCPForIPv4()
	dataLength := SwapBytesUint16(hdr.PayloadLen)

	sum := calculateDataChecksum(p.Data, int(dataLength-TCPMinLen), 0)

	sum += calculateIPv6AddrChecksum(hdr) +
		uint32(dataLength) +
		uint32(hdr.Proto) +
		calculateTCPChecksum(tcp)

	return ^reduceChecksum(sum)
}

// CalculateIPv4ICMPChecksum calculates ICMP checksum in case if L3 protocol is IPv4.
func CalculateIPv4ICMPChecksum(p *Packet) uint16 {
	hdr := p.GetIPv4()
	dataLength := SwapBytesUint16(hdr.TotalLength) - IPv4MinLen

	sum := calculateDataChecksum(unsafe.Pointer(p.GetICMPForIPv4()), int(dataLength), 0)

	return ^reduceChecksum(sum)
}

// CalculateIPv6ICMPChecksum calculates ICMP checksum in case if L3 protocol is IPv6.
func CalculateIPv6ICMPChecksum(p *Packet) uint16 {
	hdr := p.GetIPv6()
	dataLength := SwapBytesUint16(hdr.PayloadLen)

	sum := calculateDataChecksum(unsafe.Pointer(p.GetICMPForIPv6()), int(dataLength), 0)

	return ^reduceChecksum(sum)
}
