// Copyright 2017 Intel Corporation.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package flow provides functionality for constructing packet processing graph

// Preparations of construction:
// All construction should be between SystemInit and SystemStart functions.
// User command line options should be added as flags before SystemInit option - it will
// parse them as well as internal library options.

// Packet processing graph construction:
// YANFF library provides nine so-called Flow Functions for packet processing graph
// construction. They operate term "flow" however it is just abstraction for connecting
// them. Not anything beyond this. These nine flow functions are:
// Receive, Generate - for adding packets to graph
// Send, Stop - for removing packets from graph
// Handle - for handling packets inside graph
// Separate, Split, Count, Merge for combining flows inside graph
// All this functions can be added to the graph be "Set" functions like
// SetReceiver, SetSplitter, etc.

// Flow functions Generate, Handle, Separate and Split use user defined functions
// for processing. These functions are received each packet from flow (or new
// allocated packet in generate). Function types of user defined functions are
// also defined in this file.

// This is the main file of YANFF library and should be always imported by
// user application.
package flow

import (
	"bytes"
	"encoding/binary"
	"github.com/intel-go/yanff/asm"
	"github.com/intel-go/yanff/common"
	"github.com/intel-go/yanff/low"
	"github.com/intel-go/yanff/packet"
	"github.com/intel-go/yanff/scheduler"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
	"unsafe"
)

var openFlowsNumber = uint32(0)
var ringName = 1
var ffCount = 0
var createdPorts []port

type UserContext scheduler.UserContext

// Flow is an abstraction for connecting flow functions with each other.
// Flow shouldn't be understood in any way beyond this.
type Flow struct {
	current *low.Queue
}

// Function type for user defined function which generates packets.
// Function receives preallocated packet where user should add
// its size and content.
type GenerateFunction func(*packet.Packet, UserContext)
type VectorGenerateFunction func([]*packet.Packet, uint, UserContext)

// Function type for user defined function which handles packets.
// Function receives a packet from flow. User should parse it
// and make necessary changes. It is prohibit to free packet in this
// function.
type HandleFunction func(*packet.Packet, UserContext)
type VectorHandleFunction func([]*packet.Packet, uint, UserContext)

// Function type for user defined function which separates packets
// based on some rule for two flows. Functions receives a packet from flow.
// User should parse it and decide whether this packet should remains in
// this flow - return true, or should be sent to new added flow - return false.
type SeparateFunction func(*packet.Packet, UserContext) bool
type VectorSeparateFunction func([]*packet.Packet, []bool, uint, UserContext)

// Function type for user defined function which splits packets
// based in some rule for multiple flows. Function receives a packet from
// flow. User should parse it and decide in which output flows this packet
// should be sent. Return number of flow shouldn't exceed target number
// which was put to SetSplitter function. Also it is assumed that "0"
// output flow is used for dropping packets - "Stop" function should be
// set after "Split" function in it.
type SplitFunction func(*packet.Packet, UserContext) uint

type receiveParameters struct {
	out   *low.Queue
	queue uint16
	port  uint8
}

func makeReceiver(port uint8, queue uint16, out *low.Queue) *scheduler.FlowFunction {
	par := new(receiveParameters)
	par.port = port
	par.queue = queue
	par.out = out
	ffCount++
	return schedState.NewUnclonableFlowFunction("receiver", ffCount, receive, par)
}

type generateParameters struct {
	out                    *low.Queue
	targetSpeed            uint64
	generateFunction       GenerateFunction
	vectorGenerateFunction VectorGenerateFunction
	mempool                *low.Mempool
}

func makeGeneratorOne(out *low.Queue, generateFunction GenerateFunction) *scheduler.FlowFunction {
	var par *generateParameters = new(generateParameters)
	par.out = out
	par.generateFunction = generateFunction
	par.mempool = low.CreateMempool()
	ffCount++
	return schedState.NewUnclonableFlowFunction("generator", ffCount, generateOne, par)
}

func makeGeneratorPerf(out *low.Queue, generateFunction GenerateFunction,
	vectorGenerateFunction VectorGenerateFunction, targetSpeed uint64, context UserContext) *scheduler.FlowFunction {
	var par *generateParameters = new(generateParameters)
	par.out = out
	par.generateFunction = generateFunction
	par.mempool = low.CreateMempool()
	par.vectorGenerateFunction = vectorGenerateFunction
	par.targetSpeed = targetSpeed
	ffCount++
	return schedState.NewClonableFlowFunction("fast generator", ffCount, generatePerf, par, generateCheck, make(chan uint64, 50), context)
}

type sendParameters struct {
	in    *low.Queue
	queue uint16
	port  uint8
}

func makeSender(port uint8, queue uint16, in *low.Queue) *scheduler.FlowFunction {
	par := new(sendParameters)
	par.port = port
	par.queue = queue
	par.in = in
	ffCount++
	return schedState.NewUnclonableFlowFunction("sender", ffCount, send, par)
}

type partitionParameters struct {
	in        *low.Queue
	outFirst  *low.Queue
	outSecond *low.Queue
	N         uint64
	M         uint64
}

func makePartitioner(in *low.Queue, outFirst *low.Queue, outSecond *low.Queue, N uint64, M uint64) *scheduler.FlowFunction {
	par := new(partitionParameters)
	par.in = in
	par.outFirst = outFirst
	par.outSecond = outSecond
	par.N = N
	par.M = M
	ffCount++
	return schedState.NewUnclonableFlowFunction("partitioner", ffCount, partition, par)
}

type separateParameters struct {
	in                     *low.Queue
	outTrue                *low.Queue
	outFalse               *low.Queue
	separateFunction       SeparateFunction
	vectorSeparateFunction VectorSeparateFunction
}

func makeSeparator(in *low.Queue, outTrue *low.Queue, outFalse *low.Queue,
	separateFunction SeparateFunction, vectorSeparateFunction VectorSeparateFunction,
	name string, context UserContext) *scheduler.FlowFunction {
	par := new(separateParameters)
	par.in = in
	par.outTrue = outTrue
	par.outFalse = outFalse
	par.separateFunction = separateFunction
	par.vectorSeparateFunction = vectorSeparateFunction
	ffCount++
	return schedState.NewClonableFlowFunction(name, ffCount, separate, par, separateCheck, make(chan uint64, 50), context)
}

type splitParameters struct {
	in            *low.Queue
	outs          []*low.Queue
	splitFunction SplitFunction
	flowNumber    uint
}

func makeSplitter(in *low.Queue, outs []*low.Queue,
	splitFunction SplitFunction, flowNumber uint, context UserContext) *scheduler.FlowFunction {
	par := new(splitParameters)
	par.in = in
	par.outs = outs
	par.splitFunction = splitFunction
	par.flowNumber = flowNumber
	ffCount++
	return schedState.NewClonableFlowFunction("splitter", ffCount, split, par, splitCheck, make(chan uint64, 50), context)
}

type handleParameters struct {
	in                   *low.Queue
	out                  *low.Queue
	handleFunction       HandleFunction
	vectorHandleFunction VectorHandleFunction
}

func makeHandler(in *low.Queue, out *low.Queue,
	handleFunction HandleFunction, vectorHandleFunction VectorHandleFunction,
	name string, context UserContext) *scheduler.FlowFunction {
	par := new(handleParameters)
	par.in = in
	par.out = out
	par.handleFunction = handleFunction
	par.vectorHandleFunction = vectorHandleFunction
	ffCount++
	return schedState.NewClonableFlowFunction(name, ffCount, handle, par, handleCheck, make(chan uint64, 50), context)
}

type writeParameters struct {
	in       *low.Queue
	filename string
}

func makeWriter(filename string, in *low.Queue) *scheduler.FlowFunction {
	par := new(writeParameters)
	par.in = in
	par.filename = filename
	ffCount++
	return schedState.NewUnclonableFlowFunction("writer", ffCount, write, par)
}

type readParameters struct {
	out      *low.Queue
	filename string
	mempool  *low.Mempool
	repcount int32
}

func makeReader(filename string, out *low.Queue, repcount int32) *scheduler.FlowFunction {
	par := new(readParameters)
	par.out = out
	par.filename = filename
	par.mempool = low.CreateMempool()
	par.repcount = repcount
	ffCount++
	return schedState.NewUnclonableFlowFunction("reader", ffCount, read, par)
}

var burstSize uint
var sizeMultiplier uint
var schedTime uint
var maxPacketsToClone uint32
var hwtxchecksum bool

type port struct {
	rxQueues       []bool
	txQueues       []bool
	config         int
	rxQueuesNumber uint16
	txQueuesNumber uint16
	port           uint8
}

var schedState scheduler.Scheduler

// Flow port types
const (
	inactivePort = iota
	autoPort
	manualPort
)

type Config struct {
	// Number of threads, each bound to a separate CPU core. Default
	// value is GOMAXPROCS.
	CPUCoresNumber uint
	// If true, scheduler is disabled entirely. Default value is false.
	DisableScheduler bool
	// If true, scheduler does not stop any previously cloned flow
	// function threads. Default value is false.
	PersistentClones bool
	// If true, Stop routine gets a dedicated CPU core instead of
	// running together with scheduler. Default value is false.
	StopOnDedicatedCore bool
	// Calculate IPv4, UDP and TCP checksums in hardware. This flag
	// slows down general TX processing, so it should be enabled if
	// applications intends to modify packets often, and therefore
	// needs to recalculate their checksums. If application doesn't
	// modify many packets, it may chose to calculate checksums in SW
	// and leave this flag off. Default value is false.
	HWTXChecksum bool
	// Specifies number of mbufs in mempool per port. Default value is
	// 8191.
	MbufNumber uint
	// Specifies number of mbufs in per-CPU core cache in
	// mempool. Default value is 250.
	MbufCacheSize uint
	// Number of BurstSize groups in all rings. This should be power
	// of 2. Default value is 256.
	RingSize uint
	// Time between scheduler actions in miliseconds. Default value is
	// 1500.
	ScaleTime uint
	// Number of mbufs per one enqueue / dequeue from ring. Default
	// value is tested for performance and not recommended to
	// change. Default value is 32.
	BurstSize uint
	// Time in miliseconds for scheduler to check changing of flow
	// function behaviour. Default value is 10000.
	CheckTime uint
	// Time in miliseconds for scheduler to display statistics.
	// Default value is 1000.
	DebugTime uint
	// Specifies logging type. Default value is common.No |
	// common.Initialization | common.Debug.
	LogType common.LogType
	// Command line arguments to pass to DPDK initialization.
	DPDKArgs []string
}

// Initializing of system. This function should be always called before graph construction.
// defaultCPUCoresNumber is a default number of cores which will be available for scheduler
// to place flow functions and their clones. This number can be always changed by cores-number option.
// Function can panic during execution.
func SystemInit(args *Config) {
	CPUCoresNumber := uint(runtime.GOMAXPROCS(0))
	if args.CPUCoresNumber != 0 {
		CPUCoresNumber = args.CPUCoresNumber
	}

	schedulerOff := args.DisableScheduler
	schedulerOffRemove := args.PersistentClones
	stopDedicatedCore := args.StopOnDedicatedCore
	hwtxchecksum = args.HWTXChecksum

	mbufNumber := uint(4 * 8191)
	if args.MbufNumber != 0 {
		mbufNumber = args.MbufNumber
	}

	mbufCacheSize := uint(250)
	if args.MbufCacheSize != 0 {
		mbufCacheSize = args.MbufCacheSize
	}

	sizeMultiplier = 256
	if args.RingSize != 0 {
		sizeMultiplier = args.RingSize
	}

	schedTime = 1500
	if args.ScaleTime != 0 {
		schedTime = args.ScaleTime
	}

	burstSize = 32
	if args.BurstSize != 0 {
		burstSize = args.BurstSize
	}

	checkTime := uint(10000)
	if args.CheckTime != 0 {
		checkTime = args.CheckTime
	}

	debugTime := uint(1000)
	if args.DebugTime != 0 {
		debugTime = args.DebugTime
	}

	logType := common.No | common.Initialization | common.Debug
	if args.LogType != 0 {
		logType = args.LogType
	}
	common.SetLogType(logType)

	argc, argv := low.InitDPDKArguments(args.DPDKArgs)
	// We want to add new clone if input ring is approximately 80% full
	maxPacketsToClone = uint32(sizeMultiplier * burstSize / 5 * 4)
	// TODO all low level initialization here! Now everything is default.
	// Init eal
	common.LogTitle(common.Initialization, "------------***-------- Initializing DPDK --------***------------")
	low.InitDPDK(argc, argv, burstSize, mbufNumber, mbufCacheSize)
	// Init Ports
	common.LogTitle(common.Initialization, "------------***-------- Initializing ports -------***------------")
	createdPorts = make([]port, low.GetPortsNumber(), low.GetPortsNumber())
	for i := range createdPorts {
		createdPorts[i].port = uint8(i)
		createdPorts[i].config = inactivePort
	}
	// Init scheduler
	common.LogTitle(common.Initialization, "------------***------ Initializing scheduler -----***------------")
	StopRing := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	schedState = scheduler.NewScheduler(CPUCoresNumber, schedulerOff, schedulerOffRemove, stopDedicatedCore, StopRing, checkTime, debugTime)
	common.LogTitle(common.Initialization, "------------***------ Filling FlowFunctions ------***------------")
	// Init packet processing
	packet.SetHWTXChecksumFlag(hwtxchecksum)
}

// Starting system - begin packet receiving and packet sending.
// This functions should be always called after flow graph construction.
// Function can panic during execution.
func SystemStart() {
	common.LogTitle(common.Initialization, "------------***--------- Checking system ---------***------------")
	checkSystem()
	common.LogTitle(common.Initialization, "------------***---------- Creating ports ---------***------------")
	for i := range createdPorts {
		if createdPorts[i].config != inactivePort {
			low.CreatePort(createdPorts[i].port, createdPorts[i].rxQueuesNumber, createdPorts[i].txQueuesNumber, hwtxchecksum)
		}
	}
	// Timeout is needed for ports to start up. This way is used in pktgen.
	// Pktgen also has checks for link status for all ports, but we compensate it
	// by additional time.
	// Timeout prevents loss of starting packets in generated flow.
	time.Sleep(time.Second * 2)

	common.LogTitle(common.Initialization, "------------***------ Starting FlowFunctions -----***------------")
	schedState.SystemStart()
	common.LogTitle(common.Initialization, "------------***--------- YANFF-GO Started --------***------------")
	schedState.Schedule(schedTime)
}

func generateRingName() string {
	s := strconv.Itoa(ringName)
	ringName++
	return s
}

// Add write function to flow graph.
// Gets flow which packets will be written to file and
// target file name.
// Function can panic during execution.
func SetWriter(IN *Flow, filename string) {
	checkFlow(IN)
	write := makeWriter(filename, IN.current)
	schedState.UnClonable = append(schedState.UnClonable, write)
	IN.current = nil
	openFlowsNumber--
}

// Add read function to flow graph.
// Gets name of pcap formatted file and number of reads. If repcount = -1,
// file is read infinitely in circle.
// Returns new opened flow with read packets.
// Function can panic during execution.
func SetReader(filename string, repcount int32) (OUT *Flow) {
	ring := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	read := makeReader(filename, ring, repcount)
	schedState.UnClonable = append(schedState.UnClonable, read)
	OUT = new(Flow)
	OUT.current = ring
	openFlowsNumber++
	return OUT
}

// Add receive function to flow graph.
// Gets port number from which packets will be received.
// Receive queue will be added to port automatically.
// Returns new opened flow with received packets
// Function can panic during execution.
func SetReceiver(port uint8) (OUT *Flow) {
	if port >= uint8(len(createdPorts)) {
		common.LogError(common.Initialization, "Requested receive port exceeds number of ports which can be used by DPDK (bind to DPDK).")
	}
	if createdPorts[port].config == manualPort {
		common.LogError(common.Initialization, "Requested receive port was previously configured as manual port. It can't be used as auto port.")
	}
	createdPorts[port].config = autoPort
	createdPorts[port].rxQueues = append(createdPorts[port].rxQueues, true)
	ring := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	recv := makeReceiver(port, createdPorts[port].rxQueuesNumber, ring)
	schedState.UnClonable = append(schedState.UnClonable, recv)
	OUT = new(Flow)
	OUT.current = ring
	openFlowsNumber++
	createdPorts[port].rxQueuesNumber++
	return OUT
}

// Add generate function to flow graph.
// Gets user-defined generate function and target speed of generation user wants to achieve
// Returns new open flow with generated packets. If targetSpeed equal to zero
// single packet non-clonable flow function will be added. It can be used for waiting of
// input user packets. If targetSpeed is more than zero clonable function is added which
// tries to achieve this speed by cloning.
// Function can panic during execution.
func SetGenerator(generateFunction interface{}, targetSpeed uint64, context UserContext) (OUT *Flow) {
	ring := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	var generate *scheduler.FlowFunction
	if targetSpeed > 0 {
		if f, t := generateFunction.(func(*packet.Packet, UserContext)); t {
			generate = makeGeneratorPerf(ring, GenerateFunction(f), nil, targetSpeed, context)
		} else if f, t := generateFunction.(func([]*packet.Packet, uint, UserContext)); t {
			generate = makeGeneratorPerf(ring, nil, VectorGenerateFunction(f), targetSpeed, context)
		} else {
			common.LogError(common.Initialization, "Function argument of SetGenerator function doesn't match any applicable prototype")
		}
		schedState.Clonable = append(schedState.Clonable, generate)
	} else {
		if f, t := generateFunction.(func(*packet.Packet, UserContext)); t {
			generate = makeGeneratorOne(ring, GenerateFunction(f))
		} else {
			common.LogError(common.Initialization, "Function argument of SetGenerator function doesn't match any applicable prototype")
		}
		schedState.UnClonable = append(schedState.UnClonable, generate)
	}
	OUT = new(Flow)
	OUT.current = ring
	openFlowsNumber++
	return OUT
}

// Add send function to flow graph.
// Gets flow which will be closed and its packets will be send and port number for which packets will be sent.
// Send queue will be added to port automatically.
// Function can panic during execution.
func SetSender(IN *Flow, port uint8) {
	checkFlow(IN)
	if port >= uint8(len(createdPorts)) {
		common.LogError(common.Initialization, "Requested send port exceeds number of ports which can be used by DPDK (bind to DPDK).")
	}
	if createdPorts[port].config == manualPort {
		common.LogError(common.Initialization, "Requested send port was previously configured as manual port. It can't be used like auto port.")
	}
	createdPorts[port].config = autoPort
	createdPorts[port].txQueues = append(createdPorts[port].txQueues, true)
	send := makeSender(port, createdPorts[port].txQueuesNumber, IN.current)
	schedState.UnClonable = append(schedState.UnClonable, send)
	IN.current = nil
	openFlowsNumber--
	createdPorts[port].txQueuesNumber++
}

// Add partition function to flow graph.
// Gets input flow and N and M constants. Returns new opened flow.
// Each loop N packets will be remained in input flow, next M packets will be sent to new flow.
// It is advised not to use this function less then (75, 75) for performance reasons.
// Function can panic during execution.
func SetPartitioner(IN *Flow, N uint64, M uint64) (OUT *Flow) {
	checkFlow(IN)
	OUT = new(Flow)
	if N == 0 || M == 0 {
		common.LogWarning(common.Initialization, "One of SetPartitioner function's arguments is zero.")
	}
	ringFirst := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	ringSecond := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	openFlowsNumber++
	partition := makePartitioner(IN.current, ringFirst, ringSecond, N, M)
	// We make partition function unclonable. The most complex task is (1,1).
	// It means that if you would like to simply divide a flow
	// it is recommended to use (75,75) instead of (1,1) for performance reasons.
	schedState.UnClonable = append(schedState.UnClonable, partition)
	IN.current = ringFirst
	OUT.current = ringSecond
	return OUT
}

// Add separate function to flow graph.
// Gets flow and user defined separate function. Returns new opened flow.
// Each packet from input flow will be remain inside input packet if
// user defined function returns "true" and is sent to new flow otherwise.
// Function can panic during execution.
func SetSeparator(IN *Flow, separateFunction interface{}, context UserContext) (OUT *Flow) {
	checkFlow(IN)
	OUT = new(Flow)
	ringTrue := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	ringFalse := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	openFlowsNumber++
	var separate *scheduler.FlowFunction
	if f, t := separateFunction.(func(*packet.Packet, UserContext) bool); t {
		separate = makeSeparator(IN.current, ringTrue, ringFalse, SeparateFunction(f), nil, "separator", context)
	} else if f, t := separateFunction.(func([]*packet.Packet, []bool, uint, UserContext)); t {
		separate = makeSeparator(IN.current, ringTrue, ringFalse, nil, VectorSeparateFunction(f), "vector separator", context)
	} else {
		common.LogError(common.Initialization, "Function argument of SetSeparator function doesn't match any applicable prototype")
	}
	schedState.Clonable = append(schedState.Clonable, separate)
	IN.current = ringTrue
	OUT.current = ringFalse
	return OUT
}

// Add split function to flow graph.
// Gets flow, user defined split function and flowNumber of new flows.
// Returns array of new opened flows with corresponding length.
// Each packet from input flow will be sent to one of new flows based on
// user defined function output for this packet.
// Function can panic during execution.
func SetSplitter(IN *Flow, splitFunction SplitFunction, flowNumber uint, context UserContext) (OutArray [](*Flow)) {
	checkFlow(IN)
	OutArray = make([](*Flow), flowNumber, flowNumber)
	rings := make([](*low.Queue), flowNumber, flowNumber)
	for i := range OutArray {
		OutArray[i] = new(Flow)
		openFlowsNumber++
		rings[i] = low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
		OutArray[i].current = rings[i]
	}
	split := makeSplitter(IN.current, rings, splitFunction, flowNumber, context)
	schedState.Clonable = append(schedState.Clonable, split)
	IN.current = nil
	openFlowsNumber--
	return OutArray
}

// Add stop function to flow graph.
// Gets flow which will be closed and all packets from each will be dropped.
// Function can panic during execution.
func SetStopper(IN *Flow) {
	checkFlow(IN)
	merge(IN.current, schedState.StopRing)
	IN.current = nil
	openFlowsNumber--
}

// Add handle function to flow graph.
// Gets flow and user defined handle function. Function can receive either HandleFunction
// or SeparateFunction. If input argument is HandleFunction then each packet from
// input flow will be handle inside user defined function and sent further in the same flow.
// If input argument is SeparateFunction user defined function can return boolean value.
// If user function returns false after handling a packet it is dropped automatically.
// Function can panic during execution.
func SetHandler(IN *Flow, handleFunction interface{}, context UserContext) {
	checkFlow(IN)
	ring := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	var handle *scheduler.FlowFunction
	if f, t := handleFunction.(func(*packet.Packet, UserContext)); t {
		handle = makeHandler(IN.current, ring, HandleFunction(f), nil, "handler", context)
	} else if f, t := handleFunction.(func([]*packet.Packet, uint, UserContext)); t {
		handle = makeHandler(IN.current, ring, nil, VectorHandleFunction(f), "vector handler", context)
	} else if f, t := handleFunction.(func(*packet.Packet, UserContext) bool); t {
		handle = makeSeparator(IN.current, ring, schedState.StopRing, SeparateFunction(f), nil, "handler", context)
	} else if f, t := handleFunction.(func([]*packet.Packet, []bool, uint, UserContext)); t {
		handle = makeSeparator(IN.current, ring, schedState.StopRing, nil, VectorSeparateFunction(f), "vector handler", context)
	} else {
		common.LogError(common.Initialization, "Function argument of SetHandler function doesn't match any applicable prototype")
	}
	schedState.Clonable = append(schedState.Clonable, handle)
	IN.current = ring
}

// Add merge function to flow graph.
// Gets any number of flows. Returns new opened flow.
// All input flows will be closed. All packets from all these flows will be sent to new flow.
// This function isn't use any cores. It changes output flows of other functions at initialization stage.
func SetMerger(InArray ...*Flow) (OUT *Flow) {
	ring := low.CreateQueue(generateRingName(), burstSize*sizeMultiplier)
	for i := range InArray {
		checkFlow(InArray[i])
		merge(InArray[i].current, ring)
		InArray[i].current = nil
		openFlowsNumber--
	}
	OUT = new(Flow)
	OUT.current = ring
	openFlowsNumber++
	return OUT
}

// Returns default MAC address of an Ethernet port.
func GetPortMACAddress(port uint8) [common.EtherAddrLen]uint8 {
	return low.GetPortMACAddress(port)
}

func receive(parameters interface{}, coreId uint8) {
	srp := parameters.(*receiveParameters)
	low.Receive(srp.port, srp.queue, srp.out, coreId)
}

func generateCheck(parameters interface{}, speedPKTS uint64, debug bool) bool {
	gp := parameters.(*generateParameters)
	if speedPKTS < gp.targetSpeed {
		return true
	}
	return false
}

func generateOne(parameters interface{}, core uint8) {
	gp := parameters.(*generateParameters)
	OUT := gp.out
	generateFunction := gp.generateFunction
	mempool := gp.mempool
	low.SetAffinity(core)

	buf := make([]uintptr, 1)
	var tempPacket *packet.Packet

	for {
		low.AllocateMbufs(buf, mempool)
		tempPacket = packet.ExtractPacket(buf[0])
		generateFunction(tempPacket, nil)
		safeEnqueue(OUT, buf, 1)
	}
}

func generatePerf(parameters interface{}, stopper chan int, report chan uint64, context scheduler.UserContext) {
	gp := parameters.(*generateParameters)
	OUT := gp.out
	generateFunction := gp.generateFunction
	vectorGenerateFunction := gp.vectorGenerateFunction
	mempool := gp.mempool
	vector := (vectorGenerateFunction != nil)

	bufs := make([]uintptr, burstSize)
	var tempPacket *packet.Packet
	tempPackets := make([]*packet.Packet, burstSize)
	var currentSpeed uint64 = 0
	var tick <-chan time.Time = time.Tick(time.Duration(schedTime) * time.Millisecond)
	var pause int = 0

	for {
		select {
		case pause = <-stopper:
			if pause == -1 {
				// It is time to close this clone
				close(stopper)
				// We don't close report channel because all clones of one function use it.
				// As one function entity will be working endlessly we don't close it anywhere.
				return
			}
		case <-tick:
			report <- currentSpeed
			currentSpeed = 0
		default:
			low.AllocateMbufs(bufs, mempool)
			if vector == false {
				for i := range bufs {
					// TODO Maybe we need to prefetcht here?
					tempPacket = packet.ExtractPacket(bufs[i])
					generateFunction(tempPacket, context)
				}
			} else {
				packet.ExtractPackets(tempPackets, bufs, burstSize)
				vectorGenerateFunction(tempPackets, burstSize, context)
			}
			safeEnqueue(OUT, bufs, burstSize)
			currentSpeed = currentSpeed + uint64(burstSize)
		}
	}
}

func send(parameters interface{}, coreId uint8) {
	srp := parameters.(*sendParameters)
	low.Send(srp.port, srp.queue, srp.in, coreId)
}

func merge(from *low.Queue, to *low.Queue) {
	// We should change out rings in all flow functions which we added before
	// and change them to one "after merge" ring.
	// We don't proceed stop and send functions here because they don't have
	// out rings. Also we don't proceed merge function because they are added
	// strictly one after another. The next merge will change previous "after merge"
	// ring automatically.
	for i := range schedState.UnClonable {
		switch schedState.UnClonable[i].Parameters.(type) {
		case *receiveParameters:
			if schedState.UnClonable[i].Parameters.(*receiveParameters).out == from {
				schedState.UnClonable[i].Parameters.(*receiveParameters).out = to
			}
		case *partitionParameters:
			if schedState.UnClonable[i].Parameters.(*partitionParameters).outFirst == from {
				schedState.UnClonable[i].Parameters.(*partitionParameters).outFirst = to
			}
			if schedState.UnClonable[i].Parameters.(*partitionParameters).outSecond == from {
				schedState.UnClonable[i].Parameters.(*partitionParameters).outSecond = to
			}
		case *generateParameters:
			if schedState.UnClonable[i].Parameters.(*generateParameters).out == from {
				schedState.UnClonable[i].Parameters.(*generateParameters).out = to
			}
		}
	}
	for i := range schedState.Clonable {
		switch schedState.Clonable[i].Parameters.(type) {
		case *splitParameters:
			for j := uint(0); j < schedState.Clonable[i].Parameters.(*splitParameters).flowNumber; j++ {
				if schedState.Clonable[i].Parameters.(*splitParameters).outs[j] == from {
					schedState.Clonable[i].Parameters.(*splitParameters).outs[j] = to
				}
			}
		case *separateParameters:
			if schedState.Clonable[i].Parameters.(*separateParameters).outTrue == from {
				schedState.Clonable[i].Parameters.(*separateParameters).outTrue = to
			}
			if schedState.Clonable[i].Parameters.(*separateParameters).outFalse == from {
				schedState.Clonable[i].Parameters.(*separateParameters).outFalse = to
			}
		case *handleParameters:
			if schedState.Clonable[i].Parameters.(*handleParameters).out == from {
				schedState.Clonable[i].Parameters.(*handleParameters).out = to
			}
		case *generateParameters:
			if schedState.Clonable[i].Parameters.(*generateParameters).out == from {
				schedState.Clonable[i].Parameters.(*generateParameters).out = to
			}
		}
	}
}

func separateCheck(parameters interface{}, speedPKTS uint64, debug bool) bool {
	sp := parameters.(*separateParameters)
	IN := sp.in
	if debug == true {
		common.LogDebug(common.Debug, "Number of packets in queue for separate: ", IN.GetQueueCount())
	}
	if IN.GetQueueCount() > maxPacketsToClone {
		return true
	}
	return false
}

func separate(parameters interface{}, stopper chan int, report chan uint64, context scheduler.UserContext) {
	sp := parameters.(*separateParameters)
	IN := sp.in
	OUTTrue := sp.outTrue
	OUTFalse := sp.outFalse
	separateFunction := sp.separateFunction
	vectorSeparateFunction := sp.vectorSeparateFunction
	vector := (vectorSeparateFunction != nil)

	bufsIn := make([]uintptr, burstSize)
	bufsTrue := make([]uintptr, burstSize)
	bufsFalse := make([]uintptr, burstSize)
	ttt := make([]bool, burstSize)
	var countOfPackets uint
	var tempPacket *packet.Packet
	var tempPacketAddr uintptr
	tempPackets := make([]*packet.Packet, burstSize)
	var currentSpeed uint64
	tick := time.Tick(time.Duration(schedTime) * time.Millisecond)
	var pause int = 0

	for {
		select {
		case pause = <-stopper:
			if pause == -1 {
				// It is time to close this clone
				close(stopper)
				// We don't close report channel because all clones of one function use it.
				// As one function entity will be working endlessly we don't close it anywhere.
				return
			}
		case <-tick:
			report <- currentSpeed
			currentSpeed = 0
		default:
			n := IN.DequeueBurst(bufsIn, burstSize)
			if n == 0 {
				if pause != 0 {
					time.Sleep(time.Duration(pause) * time.Nanosecond)
				}
				continue
			}
			countOfPackets = 0
			if vector == false {
				tempPacketAddr = packet.ExtractPacketAddr(bufsIn[0])
				// TODO here and in following flow functions: Now prefetch by zero address is
				// slowing down the application. However we should use it in the future instead of code duplication.
				for i := uint(0); i < n-1; i++ {
					tempPacket = packet.ToPacket(tempPacketAddr)
					tempPacketAddr = packet.ExtractPacketAddr(bufsIn[i+1])
					asm.Prefetcht0(tempPacketAddr)
					if separateFunction(tempPacket, context) == false {
						bufsFalse[countOfPackets] = bufsIn[i]
						countOfPackets++
					} else {
						bufsTrue[uint(i)-countOfPackets] = bufsIn[i]
					}
				}
				if separateFunction(packet.ToPacket(tempPacketAddr), context) == false {
					bufsFalse[countOfPackets] = bufsIn[n-1]
					countOfPackets++
				} else {
					bufsTrue[uint(n-1)-countOfPackets] = bufsIn[n-1]
				}
			} else {
				// TODO add prefetch for vector functions
				packet.ExtractPackets(tempPackets, bufsIn, n)
				vectorSeparateFunction(tempPackets, ttt, n, context)
				for i := uint(0); i < n; i++ {
					if ttt[i] == false {
						bufsFalse[countOfPackets] = bufsIn[i]
						countOfPackets++
					} else {
						bufsTrue[uint(i)-countOfPackets] = bufsIn[i]
					}
				}
			}
			if countOfPackets != 0 {
				safeEnqueue(OUTFalse, bufsFalse, countOfPackets)
			}
			if countOfPackets != uint(n) {
				c := n - countOfPackets
				safeEnqueue(OUTTrue, bufsTrue, uint(c))
			}
			currentSpeed += uint64(n)
		}
	}
}

func partition(parameters interface{}, core uint8) {
	cp := parameters.(*partitionParameters)
	IN := cp.in
	OUTFirst := cp.outFirst
	OUTSecond := cp.outSecond
	N := cp.N
	M := cp.M

	low.SetAffinity(core)

	bufsIn := make([]uintptr, burstSize)
	bufsFirst := make([]uintptr, burstSize)
	bufsSecond := make([]uintptr, burstSize)
	var countOfPackets uint
	currentPacketNumber := uint64(0)
	sw := true
	for {
		n := IN.DequeueBurst(bufsIn, burstSize)
		if n == 0 {
			continue
		}
		countOfPackets = 0
		for i := uint(0); i < n; i++ {
			currentPacketNumber++
			if sw == true {
				bufsFirst[countOfPackets] = bufsIn[i]
				countOfPackets++
				if currentPacketNumber == N {
					sw = false
					currentPacketNumber = 0
				}
			} else {
				bufsSecond[uint(i)-countOfPackets] = bufsIn[i]
				if currentPacketNumber == M {
					sw = true
					currentPacketNumber = 0
				}
			}
		}
		if countOfPackets != 0 {
			safeEnqueue(OUTFirst, bufsFirst, countOfPackets)
		}
		if countOfPackets != uint(n) {
			c := n - countOfPackets
			safeEnqueue(OUTSecond, bufsSecond, uint(c))
		}
	}
}

func splitCheck(parameters interface{}, speedPKTS uint64, debug bool) bool {
	sp := parameters.(*splitParameters)
	IN := sp.in
	if debug == true {
		common.LogDebug(common.Debug, "Number of packets in queue for split: ", IN.GetQueueCount())
	}
	if IN.GetQueueCount() > maxPacketsToClone {
		return true
	}
	return false
}

func split(parameters interface{}, stopper chan int, report chan uint64, context scheduler.UserContext) {
	sp := parameters.(*splitParameters)
	IN := sp.in
	OUT := sp.outs
	splitFunction := sp.splitFunction
	flowNumber := sp.flowNumber

	InputMbufs := make([]uintptr, burstSize)
	OutputMbufs := make([][]uintptr, flowNumber)
	countOfPackets := make([]int, flowNumber)
	for index := range OutputMbufs {
		OutputMbufs[index] = make([]uintptr, burstSize)
		countOfPackets[index] = 0
	}
	var tempPacket *packet.Packet
	var tempPacketAddr uintptr
	var currentSpeed uint64
	tick := time.Tick(time.Duration(schedTime) * time.Millisecond)
	var pause int = 0

	for {
		select {
		case pause = <-stopper:
			if pause == -1 {
				// It is time to close this clone
				close(stopper)
				// We don't close report channel because all clones of one function use it.
				// As one function entity will be working endlessly we don't close it anywhere.
				return
			}
		case <-tick:
			report <- currentSpeed
			currentSpeed = 0
		default:
			n := IN.DequeueBurst(InputMbufs, burstSize)
			if n == 0 {
				if pause != 0 {
					time.Sleep(time.Duration(pause) * time.Nanosecond)
				}
				continue
			}
			tempPacketAddr = packet.ExtractPacketAddr(InputMbufs[0])
			for i := uint(0); i < n-1; i++ {
				tempPacket = packet.ToPacket(tempPacketAddr)
				tempPacketAddr = packet.ExtractPacketAddr(InputMbufs[i+1])
				asm.Prefetcht0(tempPacketAddr)
				index := splitFunction(tempPacket, context)
				OutputMbufs[index][countOfPackets[index]] = InputMbufs[i]
				countOfPackets[index]++
			}
			index := splitFunction(packet.ToPacket(tempPacketAddr), context)
			OutputMbufs[index][countOfPackets[index]] = InputMbufs[n-1]
			countOfPackets[index]++

			for index := uint(0); index < flowNumber; index++ {
				if countOfPackets[index] == 0 {
					continue
				}
				safeEnqueue(OUT[index], OutputMbufs[index], uint(countOfPackets[index]))
				currentSpeed += uint64(countOfPackets[index])
				countOfPackets[index] = 0
			}
		}
	}
}

func handleCheck(parameters interface{}, speedPKTS uint64, debug bool) bool {
	sp := parameters.(*handleParameters)
	IN := sp.in
	if debug == true {
		common.LogDebug(common.Debug, "Number of packets in queue for handle: ", IN.GetQueueCount())
	}
	if IN.GetQueueCount() > maxPacketsToClone {
		return true
	}
	return false
}

func handle(parameters interface{}, stopper chan int, report chan uint64, context scheduler.UserContext) {
	sp := parameters.(*handleParameters)
	IN := sp.in
	OUT := sp.out
	handleFunction := sp.handleFunction
	vectorHandleFunction := sp.vectorHandleFunction
	vector := (vectorHandleFunction != nil)

	bufs := make([]uintptr, burstSize)
	var tempPacket *packet.Packet
	var tempPacketAddr uintptr
	tempPackets := make([]*packet.Packet, burstSize)
	var currentSpeed uint64
	tick := time.Tick(time.Duration(schedTime) * time.Millisecond)
	var pause int = 0

	for {
		select {
		case pause = <-stopper:
			if pause == -1 {
				// It is time to close this clone
				close(stopper)
				// We don't close report channel because all clones of one function use it.
				// As one function entity will be working endlessly we don't close it anywhere.
				return
			}
		case <-tick:
			report <- currentSpeed
			currentSpeed = 0
		default:
			n := IN.DequeueBurst(bufs, burstSize)
			if n == 0 {
				if pause != 0 {
					time.Sleep(time.Duration(pause) * time.Nanosecond)
				}
				continue
			}
			if vector == false {
				tempPacketAddr = packet.ExtractPacketAddr(bufs[0])
				for i := uint(0); i < n-1; i++ {
					tempPacket = packet.ToPacket(tempPacketAddr)
					tempPacketAddr = packet.ExtractPacketAddr(bufs[i+1])
					asm.Prefetcht0(tempPacketAddr)
					handleFunction(tempPacket, context)
				}
				handleFunction(packet.ToPacket(tempPacketAddr), context)
			} else {
				// TODO add prefetch for vector functions
				packet.ExtractPackets(tempPackets, bufs, n)
				vectorHandleFunction(tempPackets, n, context)
			}
			safeEnqueue(OUT, bufs, uint(n))
			currentSpeed += uint64(n)
		}
	}
}

func write(parameters interface{}, coreId uint8) {
	wp := parameters.(*writeParameters)
	IN := wp.in
	filename := wp.filename

	bufIn := make([]uintptr, 1)
	var tempPacket *packet.Packet

	f, err := os.Create(filename)
	check(err)
	defer f.Close()

	WritePcapGlobalHdr(f)
	for {
		n := IN.DequeueBurst(bufIn, 1)
		if n == 0 {
			continue
		}
		tempPacket = packet.ExtractPacket(bufIn[0])
		WritePcapOnePacket(tempPacket, f)
	}
}

func read(parameters interface{}, coreId uint8) {
	rp := parameters.(*readParameters)
	OUT := rp.out
	filename := rp.filename
	mempool := rp.mempool
	repcount := rp.repcount

	buf := make([]uintptr, 1)
	var tempPacket *packet.Packet

	f, err := os.Open(filename)
	check(err)
	defer f.Close()

	// Read pcap gloabl header once
	var glHdr pcapGlobHdr
	readPcapGlobalHdr(f, &glHdr)

	count := int32(0)

	for {
		low.AllocateMbufs(buf, mempool)
		tempPacket = packet.ExtractPacket(buf[0])
		isEOF := readOnePacket(tempPacket, f)
		if isEOF {
			atomic.AddInt32(&count, 1)
			if count == repcount {
				break
			}
			_, err := f.Seek(int64(unsafe.Sizeof(glHdr)), 0)
			check(err)
			readOnePacket(tempPacket, f)
		}
		safeEnqueue(OUT, buf, 1)
	}
}

// Pcap global header
type pcapGlobHdr struct {
	MagicNumber  uint32 /* magic number */
	VersionMajor uint16 /* major version number */
	VersionMinor uint16 /* minor version number */
	Thiszone     int32  /* GMT to local correction */
	Sigfigs      uint32 /* accuracy of timestamps */
	Snaplen      uint32 /* max length of captured packets, in octets */
	Network      uint32 /* data link type */
}

// Pcap packet header
type pcapRecHdr struct {
	TsSec   uint32 /* timestamp seconds */
	TsUsec  uint32 /* timestamp microseconds */
	InclLen uint32 /* number of octets of packet saved in file */
	OrigLen uint32 /* actual length of packet */
}

// Writes global pcap header into file.
func WritePcapGlobalHdr(f *os.File) {
	glHdr := pcapGlobHdr{
		MagicNumber:  0xa1b2c3d4,
		VersionMajor: 2,
		VersionMinor: 4,
		Snaplen:      65535,
		Network:      1,
	}
	buffer := bytes.Buffer{}
	err := binary.Write(&buffer, binary.LittleEndian, &glHdr)
	check(err)

	_, err = f.Write(buffer.Bytes())
	check(err)
}

// Write one packet with pcap header in file. Assumes global
// pcap header is already present in file.
func WritePcapOnePacket(pkt *packet.Packet, f *os.File) {
	bytes := low.GetRawPacketBytesMbuf(pkt.CMbuf)
	writePcapRecHdr(f, bytes)
	writePacketBytes(f, bytes)
}

func writePcapRecHdr(f *os.File, pktBytes []byte) error {
	t := time.Now()
	hdr := pcapRecHdr{
		TsSec:   uint32(t.Unix()),
		TsUsec:  uint32(t.UnixNano() - t.Unix()*1e9),
		InclLen: uint32(len(pktBytes)),
		OrigLen: uint32(len(pktBytes)),
	}
	buffer := bytes.Buffer{}
	err := binary.Write(&buffer, binary.LittleEndian, &hdr)
	check(err)

	_, err = f.Write(buffer.Bytes())
	check(err)
	return nil
}

func writePacketBytes(f *os.File, pktBytes []byte) {
	_, err := f.Write(pktBytes)
	check(err)
}

func readPcapGlobalHdr(f *os.File, glHdr *pcapGlobHdr) {
	data := make([]byte, unsafe.Sizeof(*glHdr))
	_, err := f.Read(data)
	check(err)

	buffer := bytes.NewBuffer(data)
	err = binary.Read(buffer, binary.LittleEndian, glHdr)
	check(err)
}

func readPcapRecHdr(f *os.File, hdr *pcapRecHdr) error {
	data := make([]byte, unsafe.Sizeof(*hdr))
	_, err := f.Read(data)

	if err != nil {
		return err
	}

	buffer := bytes.NewBuffer(data)
	err = binary.Read(buffer, binary.LittleEndian, hdr)
	check(err)
	return nil
}

func readPacketBytes(f *os.File, inclLen uint32) []byte {
	pkt := make([]byte, inclLen)
	_, err := f.Read(pkt)
	check(err)
	return pkt
}

func readOnePacket(pkt *packet.Packet, f *os.File) bool {
	var hdr pcapRecHdr
	err := readPcapRecHdr(f, &hdr)
	if err == io.EOF {
		return true
	} else {
		check(err)
	}
	bytes := readPacketBytes(f, hdr.InclLen)
	packet.PacketFromByte(pkt, bytes)
	return false
}

// This function tries to write elements to input ring. However
// if this ring can't get these elements they will be placed
// inside stop ring which is emptied in separate thread.
func safeEnqueue(place *low.Queue, data []uintptr, number uint) {
	done := place.EnqueueBurst(data, number)
	if done < number {
		schedState.Dropped += number - uint(done)
		done2 := schedState.StopRing.EnqueueBurst(data[done:number], number-uint(done))
		// If stop ring is crowded a function will call C stop directly without
		// moving forward. It prevents constant crowd stop and increases
		// performance on "long accelerating" topologies in 1.5x times.
		if done2 < number-uint(done) {
			common.LogWarning(common.Verbose, "Normal fast stop is crowded. Use slow C stop instead.")
			low.DirectStop(int(number-uint(done)-uint(done2)), data[done+done2:number])
		}
	}
	// TODO we need to investigate whether we need to return actual number of enqueued packets.
	// We can use this number if controlling speed, however it is not clear what is better:
	// to use actual number or to use simply number of packets processed by a function like now.
}

func checkFlow(f *Flow) {
	if f.current == nil {
		common.LogError(common.Initialization, "One of the flows is used after it was closed!")
	}
}

func checkSystem() {
	if openFlowsNumber != 0 {
		common.LogError(common.Initialization, "Some flows are left open at the end of configuration!")
	}
	for i := range createdPorts {
		if createdPorts[i].config == inactivePort {
			continue
		}
		if createdPorts[i].rxQueuesNumber == 0 && createdPorts[i].txQueuesNumber == 0 {
			common.LogError(common.Initialization, "Port", createdPorts[i].port, "has no send and receive queues. It is an error in DPDK.")
		}
		for j := range createdPorts[i].rxQueues {
			if createdPorts[i].rxQueues[j] != true {
				common.LogError(common.Initialization, "Port", createdPorts[i].port, "doesn't use all receive queues, packets can be missed due to RSS!")
			}
		}
		for j := range createdPorts[i].txQueues {
			if createdPorts[i].txQueues[j] != true {
				common.LogWarning(common.Initialization, "Port", createdPorts[i].port, "has unused send queue. Performance can be lower than it is expected!")
			}
		}
	}
}

func check(e error) {
	if e != nil {
		panic(e)
	}
}
