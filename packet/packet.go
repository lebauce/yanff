// Copyright 2017 Intel Corporation.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package packet provides functionality for fast parsing and generating
// of packets with known structure.
// The following header types are supported:
//	* L2 Ethernet
//	* L3 IPv4 and IPv6
//	* L4 TCP and UDP
// At the moment IPv6 is supported without extension headers.
//
// For performance
// reasons YANFF provides a set of functions each of them parse exact network level.
//
// Packet parsing
//
// Family of parsing functions can parse packets with known structure of headers
// and parse exactly required protocols.
//
// YANFF provides two groups of parsing functions:
// conditional and unconditional parsing functions.
//
// Unconditional parsing functions are used to parse exactly required protocols. They use constant offsets and
// pointer arithmetic to get required pointers. There are no checks for correctness because of performance reasons.
//
// Conditional parsing functions are used to parse any supported protocols.
//
// Packet generation
//
// Packets should be placed in special memory to be sent, so user should use
// additional functions to generate packets. There are two possibilities to do this:
//		GeneratePacketFromByte function
// This function get slice of bytes of any size. Returns packet which contains only these bytes.
//		CreateEmpty function family
// There is family of functions to generate empty packets of predefined
// size with known header types. All these functions return empty but parsed
// packet with required protocol headers and preallocated space for payload.
// All these functions get size of payload as argument. After using one of
// this functions user can fill any required fields headers of the packet and
// also fill payload.
package packet

import (
	"fmt"
	"unsafe"

	. "github.com/intel-go/yanff/common"
	"github.com/intel-go/yanff/low"
)

var mbufStructSize uintptr
var hwtxchecksum bool

func init() {
	var t1 low.Mbuf
	mbufStructSize = unsafe.Sizeof(t1)
	var t2 Packet
	packetStructSize := unsafe.Sizeof(t2)
	low.SetPacketStructSize(int(packetStructSize))
}

// TODO Add function to write user data after headers and set "data" field

// The following structures must be consistent with these C duplications

// EtherHdr L2 header from DPDK: lib/librte_ether/rte_ehter.h
type EtherHdr struct {
	DAddr     [EtherAddrLen]uint8 // Destination address
	SAddr     [EtherAddrLen]uint8 // Source address
	EtherType uint16              // Frame type
}

func (hdr *EtherHdr) String() string {
	r0 := "L2 protocol: Ethernet\n"
	s := hdr.SAddr
	r1 := fmt.Sprintf("Ethernet Source: %02x:%02x:%02x:%02x:%02x:%02x\n", s[0], s[1], s[2], s[3], s[4], s[5])
	d := hdr.DAddr
	r2 := fmt.Sprintf("Ethernet Destination: %02x:%02x:%02x:%02x:%02x:%02x\n", d[0], d[1], d[2], d[3], d[4], d[5])
	return r0 + r1 + r2
}

// IPv4Hdr L3 header from DPDK: lib/librte_net/rte_ip.h
type IPv4Hdr struct {
	VersionIhl     uint8  // version and header length
	TypeOfService  uint8  // type of service
	TotalLength    uint16 // length of packet
	PacketID       uint16 // packet ID
	FragmentOffset uint16 // fragmentation offset
	TimeToLive     uint8  // time to live
	NextProtoID    uint8  // protocol ID
	HdrChecksum    uint16 // header checksum
	SrcAddr        uint32 // source address
	DstAddr        uint32 // destination address
}

func (hdr *IPv4Hdr) String() string {
	r0 := "    L3 protocol: IPv4\n"
	s := hdr.SrcAddr
	r1 := fmt.Sprintln("    IPv4 Source:", byte(s), ":", byte(s>>8), ":", byte(s>>16), ":", byte(s>>24))
	d := hdr.DstAddr
	r2 := fmt.Sprintln("    IPv4 Destination:", byte(d), ":", byte(d>>8), ":", byte(d>>16), ":", byte(d>>24))
	return r0 + r1 + r2
}

// IPv6Hdr L3 header from DPDK: lib/librte_net/rte_ip.h
type IPv6Hdr struct {
	VtcFlow    uint32             // IP version, traffic class & flow label
	PayloadLen uint16             // IP packet length - includes sizeof(ip_header)
	Proto      uint8              // Protocol, next header
	HopLimits  uint8              // Hop limits
	SrcAddr    [IPv6AddrLen]uint8 // IP address of source host
	DstAddr    [IPv6AddrLen]uint8 // IP address of destination host(s)
}

func (hdr *IPv6Hdr) String() string {
	r0 := "    L3 protocol: IPv6\n"
	s := hdr.SrcAddr
	r1 := fmt.Sprintf("    IPv6 Source: %02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x\n", s[0], s[1], s[2], s[3], s[4], s[5], s[6], s[7], s[8], s[9], s[10], s[11], s[12], s[13], s[14], s[15])
	d := hdr.DstAddr
	r2 := fmt.Sprintf("    IPv6 Destination %02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x\n", d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7], d[8], d[9], d[10], d[11], d[12], d[13], d[14], d[15])
	return r0 + r1 + r2
}

// TCPHdr L4 header from DPDK: lib/librte_net/rte_tcp.h
type TCPHdr struct {
	SrcPort  uint16   // TCP source port
	DstPort  uint16   // TCP destination port
	SentSeq  uint32   // TX data sequence number
	RecvAck  uint32   // RX data acknowledgement sequence number
	DataOff  uint8    // Data offset
	TCPFlags TCPFlags // TCP flags
	RxWin    uint16   // RX flow control window
	Cksum    uint16   // TCP checksum
	TCPUrp   uint16   // TCP urgent pointer, if any
}

func (hdr *TCPHdr) String() string {
	r0 := "        L4 protocol: TCP\n"
	r1 := fmt.Sprintf("        L4 Source: %d\n", SwapBytesUint16(hdr.SrcPort))
	r2 := fmt.Sprintf("        L4 Destination: %d\n", SwapBytesUint16(hdr.DstPort))
	return r0 + r1 + r2
}

// UDPHdr L4 header from DPDK: lib/librte_net/rte_udp.h
type UDPHdr struct {
	SrcPort    uint16 // UDP source port
	DstPort    uint16 // UDP destination port
	DgramLen   uint16 // UDP datagram length
	DgramCksum uint16 // UDP datagram checksum
}

func (hdr *UDPHdr) String() string {
	r0 := "        L4 protocol: UDP\n"
	r1 := fmt.Sprintf("        L4 Source: %d\n", SwapBytesUint16(hdr.SrcPort))
	r2 := fmt.Sprintf("        L4 Destination: %d\n", SwapBytesUint16(hdr.DstPort))
	return r0 + r1 + r2
}

// ICMPHdr L4 header.
type ICMPHdr struct {
	Type       uint8  // ICMP message type
	Code       uint8  // ICMP message code
	Cksum      uint16 // ICMP checksum
	Identifier uint16 // ICMP message identifier in some messages
	SeqNum     uint16 // ICMP message sequence number in some messages
}

func (hdr *ICMPHdr) String() string {
	r0 := "        L4 protocol: ICMP\n"
	r1 := fmt.Sprintf("        ICMP Type: %d\n", hdr.Type)
	r2 := fmt.Sprintf("        ICMP Code: %d\n", hdr.Code)
	r3 := fmt.Sprintf("        ICMP Cksum: %d\n", SwapBytesUint16(hdr.Cksum))
	r4 := fmt.Sprintf("        ICMP Identifier: %d\n", SwapBytesUint16(hdr.Identifier))
	r5 := fmt.Sprintf("        ICMP SeqNum: %d\n", SwapBytesUint16(hdr.SeqNum))
	return r0 + r1 + r2 + r3 + r4 + r5
}

// Packet is a set of pointers in YANFF library. Each pointer points to one of five headers:
// Mac, IPv4, IPv6, TCP and UDP plus raw pointer.
//
// Empty packet means that only raw pointer is not nil: it points to beginning of packet data
// – raw bits. User should extract packet data somehow.
//
// Parsing means to fill required header pointers with corresponding headers. For example,
// after user fills IPv4 pointer to right place inside packet he can use its fields like
// packet.IPv4.SrcAddr or packet.IPv4.DstAddr.
type Packet struct {
	L3   unsafe.Pointer // Pointer to L3 header in mbuf
	L4   unsafe.Pointer // Pointer to L4 header in mbuf
	Data unsafe.Pointer // Pointer to the packet payload data

	// Last two fields of this structure is filled during InitMbuf macros inside low.c file
	// Need to change low.c for all changes in these fields or adding/removing fields before them.
	Ether *EtherHdr // Pointer to L2 header in mbuf. It is always parsed and point beginning of packet.
	CMbuf *low.Mbuf // Private pointer to mbuf. Users shouldn't know anything about mbuf
}

func (packet *Packet) unparsed() uintptr {
	return uintptr(unsafe.Pointer(packet.Ether)) + EtherLen
}

// Start function return pointer to first byte of packet
// Which is the same as first byte of ethernet protocol header
func (packet *Packet) Start() uintptr {
	return uintptr(unsafe.Pointer(packet.Ether))
}

func (packet *Packet) ParseL3() {
	packet.L3 = packet.unparsed()
}

func (packet *Packet) GetIPv4() *IPv4Hdr {
	if packet.Ether.EtherType == SwapBytesUint16(IPV4Number) {
		return (*IPv4Hdr)(packet.L3)
	}
	return nil
}

func (packet *Packet) GetIPv6() *IPv6Hdr {
	if packet.Ether.EtherType == SwapBytesUint16(IPV6Number) {
		return (*IPv6Hdr)(packet.L3)
	}
	return nil
}

func (packet *Packet) ParseL4ForIPv4() {
	packet.L4 = unsafe.Pointer(packet.unparsed() + uintptr((packet.IPv4.VersionIhl&0x0f)<<2))
}

func (packet *Packet) ParseL4ForIPv6() {
	packet.L4 = unsafe.Pointer(packet.unparsed() + uintptr(IPv6Len))
}

func (packet *Packet) GetTCPForIPv4() *TCPHdr {
	if packet.IPv4.NextProtoID == TCPNumber {
		return (*TCPHdr)(packet.L4)
	}
	return nil
}

func (packet *Packet) GetTCPForIPv6() *TCPHdr {
	if packet.IPv6.Proto == TCPNumber {
		return (*TCPHdr)(packet.L4)
	}
	return nil
}

func (packet *Packet) GetUDPForIPv4() *UDPHdr {
	if packet.IPv4.NextProtoID == UDPNumber {
		return (*UDPHdr)(packet.L4)
	}
	return nil
}

func (packet *Packet) GetUDPForIPv6() *UDPHdr {
	if packet.IPv6.Proto == UDPNumber {
		return (*UDPHdr)(packet.L4)
	}
	return nil
}

func (packet *Packet) GetICMPForIPv4() *ICMPHdr {
	if packet.IPv4.NextProtoID == ICMPNumber {
		return (*TCPHdr)(packet.L4)
	}
	return nil
}

func (packet *Packet) GetICMPForIPv6() *ICMPHdr {
	if packet.IPv6.Proto == ICMPNumber {
		return (*UDPHdr)(packet.L4)
	}
	return nil
}

func (packet *Packet) ParseAllKnownL3() (*IPv4Hdr, *IPv6Hdr) {
	packet.ParseL3()
	return GetIPv4(), GetIPv6()
}

func (packet *Packet) ParseAllKnownL4ForIPv4() (*TCPHdr, *UDPHdr, *ICMPHdr) {
	packet.ParseL4ForIPv4()
	return GetTCPForIPv4(), GetUDPForIPv4(), GetICMPForIPv4()
}

func (packet *Packet) ParseAllKnownL4ForIPv6() (*TCPHdr, *UDPHdr, *ICMPHdr) {
	packet.ParseL4ForIPv6()
	return GetTCPForIPv6(), GetUDPForIPv6(), GetICMPForIPv6()
}

func (packet *Packet) ParseL7(uint protocol) {
	switch protocol {
	case TCPNumber:
		packet.Data = unsafe.Pointer(uintptr(packet.L4) + uintptr(packet.L4.DataOff&0xf0)>>2)
	case UDPNumber:
		packet.Data = unsafe.Pointer(uintptr(packet.L4) + uintptr(UDPLen))
	case ICMPNumber:
		packet.Data = unsafe.Pointer(uintptr(packet.L4) + uintptr(ICMPLen))
	}
}

// ExtractPacketAddr extracts packet structure from mbuf used in package flow
func ExtractPacketAddr(IN uintptr) uintptr {
	return IN + mbufStructSize
}

// ToPacket should be unexported, used in flow package
func ToPacket(IN uintptr) *Packet {
	return (*Packet)(unsafe.Pointer(IN))
}

// ExtractPacket extracts packet structure from mbuf used in package flow
func ExtractPacket(IN uintptr) *Packet {
	return ToPacket(ExtractPacketAddr(IN))
}

// SetHWTXChecksumFlag should not be exported but it is used in flow.
func SetHWTXChecksumFlag(flag bool) {
	hwtxchecksum = flag
}

// ExtractPackets creates vector of packets by calling ExtractPacket function
// is unexported, used in flow package
func ExtractPackets(packet []*Packet, IN []uintptr, n uint) {
	for i := uint(0); i < n; i++ {
		packet[i] = ExtractPacket(IN[i])
	}
}

// GeneratePacketFromByte function gets non-initialized packet and slice of bytes of any size.
// Initializes input packet and fills it with these bytes.
func GeneratePacketFromByte(packet *Packet, data []byte) bool {
	if low.AppendMbuf(packet.CMbuf, uint(len(data))) == false {
		LogWarning(Debug, "GeneratePacketFromByte: Cannot append mbuf")
		return false
	}
	low.WriteDataToMbuf(packet.CMbuf, data)
	return true
}

// All following functions set Data pointer because it is assumed that user
// need to generate real packets with some information

// InitEmptyPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointer to Ethernet header.
func InitEmptyPacket(packet *Packet, plSize uint) bool {
	bufSize := plSize + EtherLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyPacket: Cannot append mbuf")
		return false
	}
	packet.Data = unsafe.Pointer(packet.unparsed())
	return true
}

// InitEmptyIPv4Packet initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet and IPv4 headers.
func InitEmptyIPv4Packet(packet *Packet, plSize uint) bool {
	// TODO After mandatory fields, IPv4 header optionally may have options of variable length
	// Now pre-allocate space only for mandatory fields
	bufSize := plSize + EtherLen + IPv4MinLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv4Packet: Cannot append mbuf")
		return false
	}
	// Set pointers to required headers. Filling headers is left for user
	packet.ParseIPv4()

	// After packet is parsed, we can write to packet struct known protocol types
	packet.Ether.EtherType = SwapBytesUint16(IPV4Number)
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv4MinLen)

	// Next fields not required by pktgen to accept packet. But set anyway
	packet.IPv4.VersionIhl = 0x45 // Ipv4, IHL = 5 (min header len)
	packet.IPv4.TotalLength = SwapBytesUint16(uint16(IPv4MinLen + plSize))

	if hwtxchecksum {
		packet.IPv4.HdrChecksum = 0
		low.SetTXIPv4OLFlags(packet.CMbuf, EtherLen, IPv4MinLen)
	}
	return true
}

// InitEmptyIPv6Packet initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet and IPv6 headers.
func InitEmptyIPv6Packet(packet *Packet, plSize uint) bool {
	bufSize := plSize + EtherLen + IPv6Len
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv6Packet: Cannot append mbuf")
		return false
	}
	packet.ParseIPv6Data()
	packet.Ether.EtherType = SwapBytesUint16(IPV6Number)
	packet.IPv6.PayloadLen = SwapBytesUint16(uint16(plSize))
	packet.IPv6.VtcFlow = SwapBytesUint32(0x60 << 24) // IP version
	return true
}

// InitEmptyIPv4TCPPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet, IPv4 and TCP headers. This function supposes that IPv4 and TCP
// headers have minimum length. In fact length can be higher due to optional fields.
// Now setting optional fields explicitly is not supported.
func InitEmptyIPv4TCPPacket(packet *Packet, plSize uint) bool {
	// Now user cannot set explicitly optional fields, so len of header is supposed to be equal to TCPMinLen
	// TODO support variable header length (ask header length from user)
	bufSize := plSize + EtherLen + IPv4MinLen + TCPMinLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyPacket: Cannot append mbuf")
		return false
	}
	// Set pointer to required headers. Filling headers is left for user
	packet.ParseIPv4()
	packet.TCP = (*TCPHdr)(unsafe.Pointer(packet.unparsed() + IPv4MinLen))
	packet.Ether.EtherType = SwapBytesUint16(IPV4Number)
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv4MinLen + TCPMinLen)

	// Next fields not required by pktgen to accept packet. But set anyway
	packet.IPv4.NextProtoID = TCPNumber
	packet.IPv4.VersionIhl = 0x45 // Ipv4, IHL = 5 (min header len)
	packet.IPv4.TotalLength = SwapBytesUint16(uint16(IPv4MinLen + TCPMinLen + plSize))
	packet.TCP.DataOff = packet.TCP.DataOff | 0x50

	if hwtxchecksum {
		packet.IPv4.HdrChecksum = 0
		low.SetTXIPv4TCPOLFlags(packet.CMbuf, EtherLen, IPv4MinLen)
	}
	return true
}

// InitEmptyIPv4UDPPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet, IPv4 and UDP headers. This function supposes that IPv4
// header has minimum length. In fact length can be higher due to optional fields.
// Now setting optional fields explicitly is not supported.
func InitEmptyIPv4UDPPacket(packet *Packet, plSize uint) bool {
	bufSize := plSize + EtherLen + IPv4MinLen + UDPLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv4UDPPacket: Cannot append mbuf")
		return false
	}
	packet.ParseIPv4()
	packet.UDP = (*UDPHdr)(unsafe.Pointer(packet.unparsed() + IPv4MinLen))
	packet.Ether.EtherType = SwapBytesUint16(IPV4Number)
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv4MinLen + UDPLen)

	// Next fields not required by pktgen to accept packet. But set anyway
	packet.IPv4.NextProtoID = UDPNumber
	packet.IPv4.VersionIhl = 0x45 // Ipv4, IHL = 5 (min header len)
	packet.IPv4.TotalLength = SwapBytesUint16(uint16(IPv4MinLen + UDPLen + plSize))
	packet.UDP.DgramLen = SwapBytesUint16(uint16(UDPLen + plSize))

	if hwtxchecksum {
		packet.IPv4.HdrChecksum = 0
		low.SetTXIPv4UDPOLFlags(packet.CMbuf, EtherLen, IPv4MinLen)
	}

	return true
}

// InitEmptyIPv4ICMPPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet, IPv4 and ICMP headers. This function supposes that IPv4
// header has minimum length. In fact length can be higher due to optional fields.
// Now setting optional fields explicitly is not supported.
func InitEmptyIPv4ICMPPacket(packet *Packet, plSize uint) bool {
	bufSize := plSize + EtherLen + IPv4MinLen + ICMPLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv4ICMPPacket: Cannot append mbuf")
		return false
	}
	packet.ParseIPv4()
	packet.ICMP = (*ICMPHdr)(unsafe.Pointer(packet.unparsed() + IPv4MinLen))
	packet.Ether.EtherType = SwapBytesUint16(IPV4Number)
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv4MinLen + ICMPLen)

	// Next fields not required by pktgen to accept packet. But set anyway
	packet.IPv4.NextProtoID = ICMPNumber
	packet.IPv4.VersionIhl = 0x45 // Ipv4, IHL = 5 (min header len)
	packet.IPv4.TotalLength = SwapBytesUint16(uint16(IPv4MinLen + ICMPLen + plSize))
	return true
}

// InitEmptyIPv6TCPPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet, IPv6 and TCP headers. This function supposes that IPv6 and TCP
// headers have minimum length. In fact length can be higher due to optional fields.
// Now setting optional fields explicitly is not supported.
func InitEmptyIPv6TCPPacket(packet *Packet, plSize uint) bool {
	// TODO support variable header length (ask header length from user)
	bufSize := plSize + EtherLen + IPv6Len + TCPMinLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv6TCPPacket: Cannot append mbuf")
		return false
	}
	packet.ParseIPv6()
	packet.TCP = (*TCPHdr)(unsafe.Pointer(packet.unparsed() + IPv6Len))
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv6Len + TCPMinLen)
	packet.Ether.EtherType = SwapBytesUint16(IPV6Number)
	packet.IPv6.Proto = TCPNumber
	packet.IPv6.PayloadLen = SwapBytesUint16(uint16(TCPMinLen + plSize))
	packet.IPv6.VtcFlow = SwapBytesUint32(0x60 << 24) // IP version
	packet.TCP.DataOff = packet.TCP.DataOff | 0x50

	if hwtxchecksum {
		low.SetTXIPv6TCPOLFlags(packet.CMbuf, EtherLen, IPv6Len)
	}
	return true
}

// InitEmptyIPv6UDPPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet, IPv6 and UDP headers. This function supposes that IPv6
// header has minimum length. In fact length can be higher due to optional fields.
// Now setting optional fields explicitly is not supported.
func InitEmptyIPv6UDPPacket(packet *Packet, plSize uint) bool {
	bufSize := plSize + EtherLen + IPv6Len + UDPLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv6UDPPacket: Cannot append mbuf")
		return false
	}
	packet.ParseIPv6()
	packet.UDP = (*UDPHdr)(unsafe.Pointer(packet.unparsed() + IPv6Len))
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv6Len + UDPLen)

	packet.Ether.EtherType = SwapBytesUint16(IPV6Number)
	packet.IPv6.Proto = UDPNumber
	packet.IPv6.PayloadLen = SwapBytesUint16(uint16(UDPLen + plSize))
	packet.IPv6.VtcFlow = SwapBytesUint32(0x60 << 24) // IP version
	packet.UDP.DgramLen = SwapBytesUint16(uint16(UDPLen + plSize))

	if hwtxchecksum {
		low.SetTXIPv6UDPOLFlags(packet.CMbuf, EtherLen, IPv6Len)
	}
	return true
}

// InitEmptyIPv6ICMPPacket initializes input packet with preallocated plSize of bytes for payload
// and init pointers to Ethernet, IPv6 and ICMP headers.
func InitEmptyIPv6ICMPPacket(packet *Packet, plSize uint) bool {
	bufSize := plSize + EtherLen + IPv6Len + ICMPLen
	if low.AppendMbuf(packet.CMbuf, bufSize) == false {
		LogWarning(Debug, "InitEmptyIPv6ICMPPacket: Cannot append mbuf")
		return false
	}
	packet.ParseIPv6()
	packet.ICMP = (*ICMPHdr)(unsafe.Pointer(packet.unparsed() + IPv6Len))
	packet.Ether.EtherType = SwapBytesUint16(IPV6Number)
	packet.Data = unsafe.Pointer(packet.unparsed() + IPv6Len + ICMPLen)

	// Next fields not required by pktgen to accept packet. But set anyway
	packet.IPv6.Proto = ICMPNumber
	packet.IPv6.PayloadLen = SwapBytesUint16(uint16(UDPLen + plSize))
	packet.IPv6.VtcFlow = SwapBytesUint32(0x60 << 24) // IP version
	return true
}

// SetHWCksumOLFlags sets hardware offloading flags to packet
func SetHWCksumOLFlags(packet *Packet) {
	if packet.Ether.EtherType == SwapBytesUint16(IPV4Number) {
		packet.IPv4.HdrChecksum = 0
		if packet.IPv4.NextProtoID == UDPNumber {
			low.SetTXIPv4UDPOLFlags(packet.CMbuf, EtherLen, IPv4MinLen)
		} else if packet.IPv4.NextProtoID == TCPNumber {
			low.SetTXIPv4TCPOLFlags(packet.CMbuf, EtherLen, IPv4MinLen)
		}
	} else if packet.Ether.EtherType == SwapBytesUint16(IPV6Number) {
		if packet.IPv6.Proto == UDPNumber {
			low.SetTXIPv6UDPOLFlags(packet.CMbuf, EtherLen, IPv6Len)
		} else if packet.IPv6.Proto == TCPNumber {
			low.SetTXIPv6TCPOLFlags(packet.CMbuf, EtherLen, IPv6Len)
		}
	}
}

// SwapBytesUint16 swaps uint16 in Little Endian and Big Endian
func SwapBytesUint16(x uint16) uint16 {
	return x<<8 | x>>8
}

// SwapBytesUint32 swaps uint32 in Little Endian and Big Endian
func SwapBytesUint32(x uint32) uint32 {
	return ((x & 0x000000ff) << 24) | ((x & 0x0000ff00) << 8) | ((x & 0x00ff0000) >> 8) | ((x & 0xff000000) >> 24)
}

// GetRawPacketBytes returns all bytes from this packet. Not zero-copy.
func (packet *Packet) GetRawPacketBytes() []byte {
	return low.GetRawPacketBytesMbuf(packet.CMbuf)
}

// GetPacketLen returns length of this packet
func (packet *Packet) GetPacketLen() uint {
	return low.GetDataLenMbuf(packet.CMbuf)
}

// EncapsulateHead adds bytes to packet. start - number of beginning byte, length - number of
// added bytes. This function should be used to add bytes to the first half
// of packet. Return false if error.
func (packet *Packet) EncapsulateHead(start uint, length uint) bool {
	if low.PrependMbuf(packet.CMbuf, length) == false {
		return false
	}
	packet.Ether = (*EtherHdr)(unsafe.Pointer(uintptr(unsafe.Pointer(packet.Ether)) - uintptr(length)))
	for i := uint(0); i < start; i++ {
		*(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i))) = *(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i+length)))
	}
	return true
}

// EncapsulateTail adds bytes to packet. start - number of beginning byte, length - number of
// added bytes. This function should be used to add bytes to the second half
// of packet. Return false if error.
func (packet *Packet) EncapsulateTail(start uint, length uint) bool {
	if low.AppendMbuf(packet.CMbuf, length) == false {
		return false
	}
	packetLength := packet.GetPacketLen()
	for i := packetLength - 1; int(i) >= int(start+length); i-- {
		*(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i))) = *(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i-length)))
	}
	return true
}

// DecapsulateHead removes bytes from packet. start - number of beginning byte, length - number of
// removed bytes. This function should be used to remove bytes from the first half
// of packet. Return false if error.
func (packet *Packet) DecapsulateHead(start uint, length uint) bool {
	if low.AdjMbuf(packet.CMbuf, length) == false {
		return false
	}
	for i := int(start - 1); i >= 0; i-- {
		*(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i+int(length)))) = *(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i)))
	}
	packet.Ether = (*EtherHdr)(unsafe.Pointer(uintptr(unsafe.Pointer(packet.Ether)) + uintptr(length)))
	return true
}

// DecapsulateTail removes bytes from packet. start - number of beginning byte, length - number of
// removed bytes. This function should be used to remove bytes from the second half
// of packet. Return false if error.
func (packet *Packet) DecapsulateTail(start uint, length uint) bool {
	packetLength := packet.GetPacketLen() // This won't be changed by next operation
	if low.TrimMbuf(packet.CMbuf, length) == false {
		return false
	}
	for i := start; i < packetLength; i++ {
		*(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i))) = *(*uint8)(unsafe.Pointer(packet.Start() + uintptr(i+length)))
	}
	return true
}

// PacketBytesChange changes packet bytes from start byte to given bytes.
// Return false if error.
func (packet *Packet) PacketBytesChange(start uint, bytes []byte) bool {
	length := uint(len(bytes))
	if start+length > packet.GetPacketLen() {
		return false
	}
	for i := uint(0); i < length; i++ {
		*(*byte)(unsafe.Pointer(packet.Start() + uintptr(start+i))) = bytes[i]
	}
	return true
}

// IPv4 converts four element address to uint32 representation
func IPv4(a byte, b byte, c byte, d byte) uint32 {
	return uint32(d)<<24 | uint32(c)<<16 | uint32(b)<<8 | uint32(a)
}
