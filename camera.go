package main

import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// ────────────────────────────────────────────────────────────
// DirectShow camera capture via go-ole
//
// Approach: Build a DirectShow filter graph:
//   Video Source → Sample Grabber (RGB24) → Null Renderer
//
// Instead of ISampleGrabberCB (which requires vtable construction),
// we poll ISampleGrabber::GetCurrentBuffer from a goroutine.
// ────────────────────────────────────────────────────────────

// DirectShow CLSIDs and IIDs
var (
	CLSID_FilterGraph           = ole.NewGUID("{E436EBB3-524F-11CE-9F53-0020AF0BA770}")
	CLSID_CaptureGraphBuilder2  = ole.NewGUID("{BF87B6E1-8C27-11D0-B3F0-00AA003761C5}")
	CLSID_SampleGrabber         = ole.NewGUID("{C1F400A0-3F08-11D3-9F0B-006008039E37}")
	CLSID_NullRenderer          = ole.NewGUID("{C1F400A4-3F08-11D3-9F0B-006008039E37}")
	CLSID_SystemDeviceEnum      = ole.NewGUID("{62BE5D10-60EB-11D0-BD3B-00A0C911CE86}")
	CLSID_VideoInputDeviceCategory = ole.NewGUID("{860BB310-5D01-11D0-BD3B-00A0C911CE86}")

	IID_IGraphBuilder          = ole.NewGUID("{56A868A9-0AD4-11CE-B03A-0020AF0BA770}")
	IID_ICaptureGraphBuilder2  = ole.NewGUID("{93E5A4E0-2D50-11D2-ABFA-00A0C9C6E38D}")
	IID_IMediaControl          = ole.NewGUID("{56A868B1-0AD4-11CE-B03A-0020AF0BA770}")
	IID_ISampleGrabber         = ole.NewGUID("{6B652FFF-11FE-4FCE-92AD-0266B5D7C78F}")
	IID_IBaseFilter            = ole.NewGUID("{56A86895-0AD4-11CE-B03A-0020AF0BA770}")
	IID_IMoniker               = ole.NewGUID("{0000000F-0000-0000-C000-000000000046}")
	IID_IPropertyBag           = ole.NewGUID("{55272A00-42CB-11CE-8135-00AA004BB851}")

	MEDIATYPE_Video = ole.NewGUID("{73646976-0000-0010-8000-00AA00389B71}")
	MEDIASUBTYPE_RGB24 = ole.NewGUID("{E436EB7D-524F-11CE-9F53-0020AF0BA770}")
)

// Camera state
type cameraSession struct {
	graphBuilder    *ole.IUnknown
	mediaControl    *ole.IUnknown
	sampleGrabber   *ole.IUnknown
	sourceFilter    *ole.IUnknown
	nullRenderer    *ole.IUnknown
	stopCh          chan struct{}
	frameCh         chan []byte // raw RGB24 frames
}

var currentCamera *cameraSession

// ────────────────────────────────────────────────────────────
// Public API
// ────────────────────────────────────────────────────────────

// scanFromCamera opens the first available camera, starts capture,
// and returns the first QR code detected, or error on timeout/failure.
// The cameraSession pointer is stored in *outCam so the main thread
// can cancel it if the user closes the preview window.
func scanFromCamera(outCam **cameraSession) (string, error) {
	cam, err := openCamera()
	if err != nil {
		return "", fmt.Errorf("打开摄像头失败: %w", err)
	}
	*outCam = cam
	defer func() {
		cam.close()
		*outCam = nil
	}()

	// Poll frames and look for QR codes
	timeout := time.After(30 * time.Second)

	for {
		select {
		case frame := <-cam.frameCh:
			if frame == nil {
				return "", fmt.Errorf("摄像头已断开")
			}
			result, err := detectQRFromFrame(frame, 640, 480)
			if err == nil && result != "" {
				return result, nil
			}
			// Continue scanning
		case <-timeout:
			return "", fmt.Errorf("扫描超时，未识别到二维码")
		case <-cam.stopCh:
			return "", fmt.Errorf("扫描已取消")
		}
	}
}

// openCamera enumerates video devices, builds the DirectShow graph,
// and starts capturing frames.
func openCamera() (*cameraSession, error) {
	if err := ole.CoInitializeEx(0, 2); err != nil {
		// Already initialized in main, ignore this error
		_ = err
	}

	cam := &cameraSession{
		stopCh:  make(chan struct{}),
		frameCh: make(chan []byte, 10),
	}

	// 1. Create Filter Graph
	graphUnknown, err := ole.CreateInstance(CLSID_FilterGraph, nil)
	if err != nil {
		return nil, fmt.Errorf("创建FilterGraph失败: %w", err)
	}
	cam.graphBuilder = graphUnknown

	// Query IGraphBuilder
	graphBuilder, err := graphUnknown.QueryInterface(IID_IGraphBuilder)
	if err != nil {
		graphUnknown.Release()
		return nil, fmt.Errorf("QueryInterface IGraphBuilder失败: %w", err)
	}

	// 2. Create CaptureGraphBuilder2
	captureUnknown, err := ole.CreateInstance(CLSID_CaptureGraphBuilder2, nil)
	if err != nil {
		graphUnknown.Release()
		return nil, fmt.Errorf("创建CaptureGraphBuilder2失败: %w", err)
	}

	captureBuilder, err := captureUnknown.QueryInterface(IID_ICaptureGraphBuilder2)
	if err != nil {
		captureUnknown.Release()
		graphUnknown.Release()
		return nil, fmt.Errorf("QueryInterface ICaptureGraphBuilder2失败: %w", err)
	}

	// Set filter graph on capture builder
	oleutil.MustCallMethod(captureBuilder, "SetFiltergraph", graphBuilder)

	// 3. Find first video capture device
	deviceMoniker, err := findFirstVideoDevice()
	if err != nil {
		captureBuilder.Release()
		graphUnknown.Release()
		return nil, fmt.Errorf("未检测到摄像头: %w", err)
	}

	// 4. Bind device moniker to get source filter
	sourceFilter, err := bindDeviceToFilter(deviceMoniker)
	deviceMoniker.Release()
	if err != nil {
		captureBuilder.Release()
		graphUnknown.Release()
		return nil, fmt.Errorf("绑定摄像头设备失败: %w", err)
	}
	cam.sourceFilter = sourceFilter

	// Add source filter to graph
	err = addFilterToGraph(graphBuilder, sourceFilter, "Video Source")
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("添加视频源失败: %w", err)
	}

	// 5. Create Sample Grabber
	sgUnknown, err := ole.CreateInstance(CLSID_SampleGrabber, nil)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("创建SampleGrabber失败: %w", err)
	}

	sampleGrabber, err := sgUnknown.QueryInterface(IID_ISampleGrabber)
	if err != nil {
		sgUnknown.Release()
		cam.cleanupGraph()
		return nil, fmt.Errorf("QueryInterface ISampleGrabber失败: %w", err)
	}
	cam.sampleGrabber = sampleGrabber

	// Add sample grabber to graph
	sgFilter, err := sgUnknown.QueryInterface(IID_IBaseFilter)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("QueryInterface IBaseFilter(SG)失败: %w", err)
	}

	err = addFilterToGraph(graphBuilder, sgFilter, "Sample Grabber")
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("添加SampleGrabber到图失败: %w", err)
	}

	// Configure sample grabber: RGB24, 640x480
	err = configureSampleGrabber(sampleGrabber)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("配置SampleGrabber失败: %w", err)
	}

	// 6. Create Null Renderer
	nullUnknown, err := ole.CreateInstance(CLSID_NullRenderer, nil)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("创建NullRenderer失败: %w", err)
	}
	cam.nullRenderer = nullUnknown

	nullFilter, err := nullUnknown.QueryInterface(IID_IBaseFilter)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("QueryInterface IBaseFilter(Null)失败: %w", err)
	}

	err = addFilterToGraph(graphBuilder, nullFilter, "Null Renderer")
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("添加NullRenderer失败: %w", err)
	}

	// 7. Connect pins: Video Source → Sample Grabber → Null Renderer
	// Use ICaptureGraphBuilder2::RenderStream
	err = renderStream(captureBuilder, sourceFilter, sgFilter, nullFilter)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("连接视频流失败: %w", err)
	}

	// 8. Get IMediaControl
	mediaControl, err := graphBuilder.QueryInterface(IID_IMediaControl)
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("QueryInterface IMediaControl失败: %w", err)
	}
	cam.mediaControl = mediaControl

	// 9. Start the graph
	_, err = oleutil.CallMethod(mediaControl, "Run")
	if err != nil {
		cam.cleanupGraph()
		return nil, fmt.Errorf("启动视频流失败: %w", err)
	}

	// 10. Start frame polling goroutine
	go cam.pollFrames(sampleGrabber)

	log.Println("Camera started successfully")
	return cam, nil
}

// ────────────────────────────────────────────────────────────
// Camera session methods
// ────────────────────────────────────────────────────────────

func (cam *cameraSession) close() {
	if cam.mediaControl != nil {
		oleutil.MustCallMethod(cam.mediaControl, "Stop")
	}
	if cam.stopCh != nil {
		select {
		case <-cam.stopCh:
			// Already closed
		default:
			close(cam.stopCh)
		}
	}
	cam.cleanupGraph()
	log.Println("Camera closed")
}

func (cam *cameraSession) cleanupGraph() {
	if cam.sampleGrabber != nil {
		cam.sampleGrabber.Release()
		cam.sampleGrabber = nil
	}
	if cam.mediaControl != nil {
		cam.mediaControl.Release()
		cam.mediaControl = nil
	}
	if cam.sourceFilter != nil {
		cam.sourceFilter.Release()
		cam.sourceFilter = nil
	}
	if cam.nullRenderer != nil {
		cam.nullRenderer.Release()
		cam.nullRenderer = nil
	}
	if cam.graphBuilder != nil {
		cam.graphBuilder.Release()
		cam.graphBuilder = nil
	}
}

// pollFrames periodically calls ISampleGrabber::GetCurrentBuffer
// and sends frames to the frame channel.
func (cam *cameraSession) pollFrames(sg *ole.IUnknown) {
	defer close(cam.frameCh)

	bufSize := 640 * 480 * 3 // RGB24
	buf := make([]byte, bufSize)

	for {
		select {
		case <-cam.stopCh:
			return
		default:
		}

		// Call ISampleGrabber::GetCurrentBuffer
		// The method is: GetCurrentBuffer([in,out] long *pBufferSize, [out] void *pBuffer)
		// In go-ole via IDispatch, we need to use a VARIANT
		sizeVar := ole.NewVariant(ole.VT_I4, int32(bufSize))
		bufVar := ole.NewVariant(ole.VT_ARRAY|ole.VT_UI1, buf)

		// Unfortunately, ISampleGrabber does not expose IDispatch.
		// We need to call directly through the vtable.
		// The ISampleGrabber vtable layout (simplified):
		//   [0-2] IUnknown: QueryInterface, AddRef, Release
		//   [3] GetConnectedMediaType
		//   [4] SetConnectedMediaType
		//   [5] GetConnectedMediaType
		//   [6] SetBufferSamples
		//   [7] GetBufferSamples
		//   [8] SetCallback
		//   [9] SetOneShot
		//   [10] SetMediaType
		//   [11] GetCurrentBuffer ← this is what we need
		//   ...

		// Call through vtable — method at index 11 (0-indexed)
		// This requires raw COM vtable access which go-ole supports
		// through IUnknown.RawVTable
		gotFrame := callISampleGrabberGetCurrentBuffer(sg, buf)

		if gotFrame {
			frameCopy := make([]byte, bufSize)
			copy(frameCopy, buf)
			select {
			case cam.frameCh <- frameCopy:
			case <-cam.stopCh:
				return
			default:
				// Frame channel full, drop frame
			}
		}

		// Throttle polling to ~15fps
		time.Sleep(66 * time.Millisecond)
	}
}

// ────────────────────────────────────────────────────────────
// DirectShow helper functions
// ────────────────────────────────────────────────────────────

func findFirstVideoDevice() (*ole.IUnknown, error) {
	devEnumUnknown, err := ole.CreateInstance(CLSID_SystemDeviceEnum, nil)
	if err != nil {
		return nil, fmt.Errorf("创建设备枚举器失败: %w", err)
	}
	defer devEnumUnknown.Release()

	// ICreateDevEnum::CreateClassEnumerator
	// We need to call through the vtable since this is a custom interface
	enumUnknown, err := createClassEnumerator(devEnumUnknown, CLSID_VideoInputDeviceCategory)
	if err != nil {
		return nil, fmt.Errorf("枚举视频设备失败: %w", err)
	}
	if enumUnknown == nil {
		return nil, fmt.Errorf("未检测到摄像头")
	}
	defer enumUnknown.Release()

	// IEnumMoniker::Next(1)
	moniker, err := enumMonikerNext(enumUnknown)
	if err != nil {
		return nil, fmt.Errorf("获取摄像头设备失败: %w", err)
	}
	if moniker == nil {
		return nil, fmt.Errorf("未检测到摄像头")
	}

	return moniker, nil
}

func bindDeviceToFilter(moniker *ole.IUnknown) (*ole.IUnknown, error) {
	// IMoniker::BindToObject
	filterUnknown, err := bindMonikerToObject(moniker)
	if err != nil {
		return nil, fmt.Errorf("绑定设备失败: %w", err)
	}
	return filterUnknown, nil
}

func addFilterToGraph(graphBuilder *ole.IUnknown, filter *ole.IUnknown, name string) error {
	// IGraphBuilder::AddFilter
	namePtr, _ := syscall.UTF16PtrFromString(name)
	return callAddFilter(graphBuilder, filter, namePtr)
}

func configureSampleGrabber(sg *ole.IUnknown) error {
	// Set media type: RGB24, 640x480
	err := callSetMediaType(sg)
	if err != nil {
		return err
	}

	// SetBufferSamples(TRUE)
	err = callSetBufferSamples(sg, true)
	if err != nil {
		return err
	}

	// SetOneShot(FALSE) — continuous capture
	err = callSetOneShot(sg, false)
	if err != nil {
		return err
	}

	return nil
}

func renderStream(captureBuilder, source, sg, nullRenderer *ole.IUnknown) error {
	// ICaptureGraphBuilder2::RenderStream
	// PIN_CATEGORY_CAPTURE = PIN_CATEGORY_PREVIEW = {FB6C4281-0353-11D1-905F-0000C0CC16BA}
	pinCategory := ole.NewGUID("{FB6C4281-0353-11D1-905F-0000C0CC16BA}")
	return callRenderStream(captureBuilder, pinCategory, MEDIATYPE_Video, source, sg, nullRenderer)
}

// ────────────────────────────────────────────────────────────
// Raw COM vtable calls (DirectShow custom interfaces)
//
// go-ole provides IDispatch-based automation, but DirectShow
// interfaces are custom (IUnknown-derived, non-dual). We access
// them through raw vtable calls using syscall.
// ────────────────────────────────────────────────────────────

// Standard COM vtable layout for IUnknown:
//   [0] QueryInterface
//   [1] AddRef
//   [2] Release
// Custom methods start at index 3.

func getVTableEntry(unk *ole.IUnknown, index uintptr) uintptr {
	// unk.RawVTable is a *uintptr pointing to the vtable
	// vtable[index] gives the function pointer
	vtable := *(**uintptr)(unsafe.Pointer(unk))
	return *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + index*unsafe.Sizeof(uintptr(0))))
}

// callVTable calls a COM method by vtable index.
// unk is the interface pointer, index is the vtable slot,
// thisPtr is the interface pointer to pass as 'this'.
func callVTable(unk *ole.IUnknown, index uintptr) uintptr {
	fn := getVTableEntry(unk, index)
	this := uintptr(unsafe.Pointer(unk))
	ret, _, _ := syscall.SyscallN(fn, this)
	return ret
}

// ────────────────────────────────────────────────────────────
// Specific DirectShow vtable calls
// ────────────────────────────────────────────────────────────

// createClassEnumerator calls ICreateDevEnum::CreateClassEnumerator (vtable[3]).
func createClassEnumerator(devEnum *ole.IUnknown, category *ole.GUID) (*ole.IUnknown, error) {
	vtable := *(**uintptr)(unsafe.Pointer(devEnum))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 3*unsafe.Sizeof(uintptr(0))))

	this := uintptr(unsafe.Pointer(devEnum))
	var enumPtr *ole.IUnknown

	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		uintptr(unsafe.Pointer(category)),
		0, // dwFlags = 0
		uintptr(unsafe.Pointer(&enumPtr)),
	)
	if ret != 0 { // S_OK = 0
		return nil, fmt.Errorf("CreateClassEnumerator HRESULT: 0x%X", ret)
	}
	return enumPtr, nil
}

// enumMonikerNext calls IEnumMoniker::Next (vtable[3]) to get one moniker.
func enumMonikerNext(enum *ole.IUnknown) (*ole.IUnknown, error) {
	vtable := *(**uintptr)(unsafe.Pointer(enum))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 3*unsafe.Sizeof(uintptr(0))))

	this := uintptr(unsafe.Pointer(enum))
	var moniker *ole.IUnknown
	var fetched uint32

	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		1, // celt = 1
		uintptr(unsafe.Pointer(&moniker)),
		uintptr(unsafe.Pointer(&fetched)),
	)
	if ret != 0 {
		return nil, fmt.Errorf("IEnumMoniker::Next HRESULT: 0x%X", ret)
	}
	return moniker, nil
}

// bindMonikerToObject calls IMoniker::BindToObject (vtable[8]).
func bindMonikerToObject(moniker *ole.IUnknown) (*ole.IUnknown, error) {
	vtable := *(**uintptr)(unsafe.Pointer(moniker))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 8*unsafe.Sizeof(uintptr(0))))

	this := uintptr(unsafe.Pointer(moniker))
	var obj *ole.IUnknown

	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		0, // pbc = NULL (bind context)
		0, // pmkToLeft = NULL
		0, // riidResult = IID_IBaseFilter (but we get IUnknown, will QI later)
		uintptr(unsafe.Pointer(&obj)),
	)
	if ret != 0 {
		return nil, fmt.Errorf("IMoniker::BindToObject HRESULT: 0x%X", ret)
	}
	return obj, nil
}

// callAddFilter calls IGraphBuilder::AddFilter (vtable[11] on IFilterGraph, which IGraphBuilder inherits).
// IFilterGraph vtable: [0-2] IUnknown, [3] AddFilter, [4] RemoveFilter, [5] EnumFilters, ...
func callAddFilter(graphBuilder *ole.IUnknown, filter *ole.IUnknown, name *uint16) error {
	vtable := *(**uintptr)(unsafe.Pointer(graphBuilder))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 3*unsafe.Sizeof(uintptr(0))))

	this := uintptr(unsafe.Pointer(graphBuilder))

	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		uintptr(unsafe.Pointer(filter)),
		uintptr(unsafe.Pointer(name)),
	)
	if ret != 0 {
		return fmt.Errorf("AddFilter HRESULT: 0x%X", ret)
	}
	return nil
}

// AM_MEDIA_TYPE structure for configuring Sample Grabber.
type AM_MEDIA_TYPE struct {
	Majortype  ole.GUID
	Subtype    ole.GUID
	BFixedSizeSamples uint32
	BTemporalCompression uint32
	LSampleSize uint32
	Formattype ole.GUID
	PUnk       uintptr
	CbFormat   uint32
	PbFormat   uintptr
}

// VIDEOINFOHEADER for format block
type VIDEOINFOHEADER struct {
	RcSource      [4]int32 // RECT
	RcTarget      [4]int32 // RECT
	DwBitRate     uint32
	DwBitErrorRate uint32
	AvgTimePerFrame int64
	BmiHeader     BITMAPINFOHEADER_WIN32
}

type BITMAPINFOHEADER_WIN32 struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func callSetMediaType(sg *ole.IUnknown) error {
	vtable := *(**uintptr)(unsafe.Pointer(sg))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 10*unsafe.Sizeof(uintptr(0)))) // SetMediaType at vtable[10]

	vih := VIDEOINFOHEADER{
		AvgTimePerFrame: 333333, // ~30fps (100ns units)
		BmiHeader: BITMAPINFOHEADER_WIN32{
			Size:    uint32(unsafe.Sizeof(BITMAPINFOHEADER_WIN32{})),
			Width:   640,
			Height:  480,
			Planes:  1,
			BitCount: 24,
			Compression: 0, // BI_RGB
			SizeImage: 640 * 480 * 3,
		},
	}

	mt := AM_MEDIA_TYPE{
		Majortype:   *MEDIATYPE_Video,
		Subtype:     *MEDIASUBTYPE_RGB24,
		BFixedSizeSamples: 1,
		LSampleSize: 640 * 480 * 3,
		Formattype:  *ole.NewGUID("{05589F80-C356-11CE-BF01-00AA0055595A}"), // FORMAT_VideoInfo
		CbFormat:    uint32(unsafe.Sizeof(vih)),
		PbFormat:    uintptr(unsafe.Pointer(&vih)),
	}

	this := uintptr(unsafe.Pointer(sg))
	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		uintptr(unsafe.Pointer(&mt)),
	)
	if ret != 0 {
		return fmt.Errorf("SetMediaType HRESULT: 0x%X", ret)
	}
	return nil
}

func callSetBufferSamples(sg *ole.IUnknown, bufferSamples bool) error {
	vtable := *(**uintptr)(unsafe.Pointer(sg))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 6*unsafe.Sizeof(uintptr(0)))) // SetBufferSamples at vtable[6]

	bVal := uintptr(0)
	if bufferSamples {
		bVal = 1
	}

	this := uintptr(unsafe.Pointer(sg))
	ret, _, _ := syscall.SyscallN(fn, this, bVal)
	if ret != 0 {
		return fmt.Errorf("SetBufferSamples HRESULT: 0x%X", ret)
	}
	return nil
}

func callSetOneShot(sg *ole.IUnknown, oneShot bool) error {
	vtable := *(**uintptr)(unsafe.Pointer(sg))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 9*unsafe.Sizeof(uintptr(0)))) // SetOneShot at vtable[9]

	bVal := uintptr(0)
	if oneShot {
		bVal = 1
	}

	this := uintptr(unsafe.Pointer(sg))
	ret, _, _ := syscall.SyscallN(fn, this, bVal)
	if ret != 0 {
		return fmt.Errorf("SetOneShot HRESULT: 0x%X", ret)
	}
	return nil
}

func callISampleGrabberGetCurrentBuffer(sg *ole.IUnknown, buf []byte) bool {
	vtable := *(**uintptr)(unsafe.Pointer(sg))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 11*unsafe.Sizeof(uintptr(0)))) // GetCurrentBuffer at vtable[11]

	bufSize := int32(len(buf))

	this := uintptr(unsafe.Pointer(sg))
	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	// S_OK = 0 means success
	return ret == 0 && bufSize > 0
}

func callRenderStream(captureBuilder *ole.IUnknown, pinCategory, mediaType *ole.GUID, source, sg, nullRenderer *ole.IUnknown) error {
	vtable := *(**uintptr)(unsafe.Pointer(captureBuilder))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + 4*unsafe.Sizeof(uintptr(0)))) // RenderStream at vtable[4]

	this := uintptr(unsafe.Pointer(captureBuilder))
	ret, _, _ := syscall.SyscallN(
		fn,
		this,
		uintptr(unsafe.Pointer(pinCategory)),
		uintptr(unsafe.Pointer(mediaType)),
		uintptr(unsafe.Pointer(source)),
		uintptr(unsafe.Pointer(sg)),
		uintptr(unsafe.Pointer(nullRenderer)),
	)
	if ret != 0 {
		return fmt.Errorf("RenderStream HRESULT: 0x%X", ret)
	}
	return nil
}
