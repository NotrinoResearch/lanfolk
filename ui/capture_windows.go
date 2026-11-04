//go:build automation && windows

package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procPrintWindow        = user32.NewProc("PrintWindow")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procCreateCompatDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatBmp    = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procGetDIBits          = gdi32.NewProc("GetDIBits")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procDwmGetWindowAttrib = dwmapi.NewProc("DwmGetWindowAttribute")
)

type rect struct{ Left, Top, Right, Bottom int32 }

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func mainWindow() (uintptr, error) {
	cls, _ := windows.UTF16PtrFromString(windowClassFor(processMode == "settings"))
	h, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(cls)), 0)
	if h == 0 {
		return 0, errors.New("the Lanfolk window was not found")
	}
	return h, nil
}

// captureWindow renders the window with PrintWindow (so it works while the
// window is covered) and returns it as PNG, cropped to the visible frame.
func captureWindow() ([]byte, error) {
	h, err := mainWindow()
	if err != nil {
		return nil, err
	}
	var wr, vis rect
	procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	const dwmwaExtendedFrameBounds = 9
	if r, _, _ := procDwmGetWindowAttrib.Call(h, dwmwaExtendedFrameBounds, uintptr(unsafe.Pointer(&vis)), unsafe.Sizeof(vis)); r != 0 {
		vis = wr
	}
	w, ht := wr.Right-wr.Left, wr.Bottom-wr.Top
	if w <= 0 || ht <= 0 {
		return nil, errors.New("the window has no size (is it minimized?)")
	}
	screen, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screen)
	mem, _, _ := procCreateCompatDC.Call(screen)
	defer procDeleteDC.Call(mem)
	bmp, _, _ := procCreateCompatBmp.Call(screen, uintptr(w), uintptr(ht))
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(mem, bmp)
	const pwRenderFullContent = 2
	procPrintWindow.Call(h, mem, pwRenderFullContent)
	procSelectObject.Call(mem, old)

	hdr := bitmapInfoHeader{Width: w, Height: -ht, Planes: 1, BitCount: 32}
	hdr.Size = uint32(unsafe.Sizeof(hdr))
	buf := make([]byte, int(w)*int(ht)*4)
	procGetDIBits.Call(mem, bmp, 0, uintptr(ht), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&hdr)), 0)

	x0, y0 := int(vis.Left-wr.Left), int(vis.Top-wr.Top)
	cw, ch := int(vis.Right-vis.Left), int(vis.Bottom-vis.Top)
	img := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			si := ((y+y0)*int(w) + (x + x0)) * 4
			di := (y*cw + x) * 4
			if si+3 >= len(buf) || si < 0 {
				continue
			}
			img.Pix[di], img.Pix[di+1], img.Pix[di+2], img.Pix[di+3] = buf[si+2], buf[si+1], buf[si], 255
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// moveWindow places the window's top-left corner at x, y (virtual screen
// coordinates, so a second monitor can be used for tests).
func moveWindow(x, y int) {
	h, err := mainWindow()
	if err != nil {
		return
	}
	const swpNoSize, swpNoZOrder, swpNoActivate = 0x0001, 0x0004, 0x0010
	procSetWindowPos.Call(h, 0, uintptr(int32(x)), uintptr(int32(y)), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}
