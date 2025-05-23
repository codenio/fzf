//go:build windows

package tui

import (
	"os"
	"syscall"
	"time"

	"github.com/junegunn/fzf/src/util"
	"golang.org/x/sys/windows"
)

const (
	timeoutInterval = 10
)

var (
	consoleFlagsInput  = uint32(windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_EXTENDED_FLAGS)
	consoleFlagsOutput = uint32(windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.ENABLE_PROCESSED_OUTPUT | windows.DISABLE_NEWLINE_AUTO_RETURN)
	counter            = uint64(0)
)

// IsLightRendererSupported checks to see if the Light renderer is supported
func IsLightRendererSupported() bool {
	var oldState uint32
	// enable vt100 emulation (https://docs.microsoft.com/en-us/windows/console/console-virtual-terminal-sequences)
	if windows.GetConsoleMode(windows.Stderr, &oldState) != nil {
		return false
	}
	// attempt to set mode to determine if we support VT 100 codes. This will work on newer Windows 10
	// version:
	canSetVt100 := windows.SetConsoleMode(windows.Stderr, oldState|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
	var checkState uint32
	if windows.GetConsoleMode(windows.Stderr, &checkState) != nil ||
		(checkState&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING {
		return false
	}
	windows.SetConsoleMode(windows.Stderr, oldState)
	return canSetVt100
}

func (r *LightRenderer) DefaultTheme() *ColorTheme {
	// the getenv check is borrowed from here: https://github.com/gdamore/tcell/commit/0c473b86d82f68226a142e96cc5a34c5a29b3690#diff-b008fcd5e6934bf31bc3d33bf49f47d8R178:
	if !IsLightRendererSupported() || os.Getenv("ConEmuPID") != "" || os.Getenv("TCELL_TRUECOLOR") == "disable" {
		return Default16
	}
	return Dark256
}

func (r *LightRenderer) initPlatform() error {
	//outHandle := windows.Stdout
	outHandle, _ := syscall.Open("CONOUT$", syscall.O_RDWR, 0)
	// enable vt100 emulation (https://docs.microsoft.com/en-us/windows/console/console-virtual-terminal-sequences)
	if err := windows.GetConsoleMode(windows.Handle(outHandle), &r.origStateOutput); err != nil {
		return err
	}
	r.outHandle = uintptr(outHandle)
	inHandle, _ := syscall.Open("CONIN$", syscall.O_RDWR, 0)
	if err := windows.GetConsoleMode(windows.Handle(inHandle), &r.origStateInput); err != nil {
		return err
	}
	r.inHandle = uintptr(inHandle)

	// channel for non-blocking reads. Buffer to make sure
	// we get the ESC sets:
	r.ttyinChannel = make(chan byte, 1024)

	r.setupTerminal()

	return nil
}

func (r *LightRenderer) closePlatform() {
	windows.SetConsoleMode(windows.Handle(r.outHandle), r.origStateOutput)
	windows.SetConsoleMode(windows.Handle(r.inHandle), r.origStateInput)
}

func openTtyIn(ttyDefault string) (*os.File, error) {
	// not used
	return nil, nil
}

func openTtyOut(ttyDefault string) (*os.File, error) {
	return os.Stderr, nil
}

func (r *LightRenderer) setupTerminal() {
	windows.SetConsoleMode(windows.Handle(r.outHandle), consoleFlagsOutput)
	windows.SetConsoleMode(windows.Handle(r.inHandle), consoleFlagsInput)

	// The following allows for non-blocking IO.
	// syscall.SetNonblock() is a NOOP under Windows.
	current := counter
	go func() {
		fd := int(r.inHandle)
		b := make([]byte, 1)
		for {
			if _, err := util.Read(fd, b); err == nil {
				r.mutex.Lock()
				// This condition prevents the goroutine from running after the renderer
				// has been closed or paused.
				if current != counter {
					r.mutex.Unlock()
					break
				}
				r.ttyinChannel <- b[0]
				// HACK: if run from PSReadline, something resets ConsoleMode to remove ENABLE_VIRTUAL_TERMINAL_INPUT.
				windows.SetConsoleMode(windows.Handle(r.inHandle), consoleFlagsInput)
				r.mutex.Unlock()
			}
		}
	}()
}

func (r *LightRenderer) restoreTerminal() {
	r.mutex.Lock()
	counter++
	// We're setting ENABLE_VIRTUAL_TERMINAL_INPUT to allow escape sequences to be read during 'execute'.
	// e.g. fzf --bind 'enter:execute:less {}'
	windows.SetConsoleMode(windows.Handle(r.inHandle), r.origStateInput|windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
	windows.SetConsoleMode(windows.Handle(r.outHandle), r.origStateOutput)
	r.mutex.Unlock()
}

func (r *LightRenderer) Size() TermSize {
	var w, h int
	var bufferInfo windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(r.outHandle), &bufferInfo); err != nil {
		w = getEnv("COLUMNS", defaultWidth)
		h = r.maxHeightFunc(getEnv("LINES", defaultHeight))

	} else {
		w = int(bufferInfo.Window.Right - bufferInfo.Window.Left)
		h = r.maxHeightFunc(int(bufferInfo.Window.Bottom - bufferInfo.Window.Top))
	}
	return TermSize{h, w, 0, 0}
}

func (r *LightRenderer) updateTerminalSize() {
	size := r.Size()
	r.width = size.Columns
	r.height = size.Lines
}

func (r *LightRenderer) findOffset() (row int, col int) {
	var bufferInfo windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(r.outHandle), &bufferInfo); err != nil {
		return -1, -1
	}
	return int(bufferInfo.CursorPosition.Y), int(bufferInfo.CursorPosition.X)
}

func (r *LightRenderer) getch(nonblock bool) (int, bool) {
	if nonblock {
		select {
		case bc := <-r.ttyinChannel:
			return int(bc), true
		case <-time.After(timeoutInterval * time.Millisecond):
			return 0, false
		}
	} else {
		bc := <-r.ttyinChannel
		return int(bc), true
	}
}
