//go:build darwin

package filename

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ebitengine/purego"
)

var native struct {
	once    sync.Once
	convert func(string) (string, error)
	err     error
}

// Key returns the host's filename spelling, using the same CoreFoundation
// filesystem conversion as Foundation. The library stays loaded for the
// process lifetime; calls only create immutable, independently released strings.
func Key() (func(string) (string, error), error) {
	native.once.Do(func() {
		cf, err := loadCoreFoundation()
		if err != nil {
			native.err = err
			return
		}
		native.convert = cf.key
	})
	return native.convert, native.err
}

type coreFoundation struct {
	create          func(uintptr, *uint16, int64) uintptr
	maximumSize     func(uintptr) int64
	filesystemBytes func(uintptr, *byte, int64) uint8
	release         func(uintptr)
}

func loadCoreFoundation() (*coreFoundation, error) {
	handle, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load native filename conversion: %w", err)
	}
	cf := new(coreFoundation)
	for _, binding := range []struct {
		name     string
		function any
	}{
		{"CFStringCreateWithCharacters", &cf.create},
		{"CFStringGetMaximumSizeOfFileSystemRepresentation", &cf.maximumSize},
		{"CFStringGetFileSystemRepresentation", &cf.filesystemBytes},
		{"CFRelease", &cf.release},
	} {
		symbol, lookupErr := purego.Dlsym(handle, binding.name)
		if lookupErr != nil {
			closeErr := purego.Dlclose(handle)
			return nil, errors.Join(fmt.Errorf("load native filename conversion %s: %w", binding.name, lookupErr), closeErr)
		}
		// Every signature above matches CoreFoundation's declared C ABI: CFIndex
		// is signed 64-bit, UniChar is uint16, and Boolean is uint8.
		purego.RegisterFunc(binding.function, symbol)
	}
	return cf, nil
}

func (cf *coreFoundation) key(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("filename is not valid UTF-8: %w", syscall.EINVAL)
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("filename contains NUL: %w", syscall.EINVAL)
	}
	ascii := true
	for i := 0; i < len(name); i++ {
		if name[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return name, nil
	}

	input := utf16.Encode([]rune(name))
	// CreateWithCharacters copies literal UTF-16 code units, preserving a
	// leading BOM. CreateWithBytes interprets that BOM as an encoding marker.
	// No Go pointer remains in CoreFoundation after this call returns.
	value := cf.create(0, &input[0], int64(len(input)))
	runtime.KeepAlive(input)
	if value == 0 {
		return "", fmt.Errorf("create native filename string: %w", syscall.EIO)
	}
	defer cf.release(value)
	maximum := cf.maximumSize(value)
	if maximum <= 0 {
		return "", fmt.Errorf("size native filename conversion: %w", syscall.EIO)
	}
	output := make([]byte, int(maximum))
	converted := cf.filesystemBytes(value, &output[0], maximum)
	runtime.KeepAlive(output)
	if converted == 0 {
		return "", fmt.Errorf("convert native filename: %w", syscall.EIO)
	}
	end := bytes.IndexByte(output, 0)
	if end <= 0 {
		return "", fmt.Errorf("native filename conversion returned an empty or unterminated name: %w", syscall.EIO)
	}
	return string(output[:end]), nil
}
