/* SPDX-License-Identifier: GPL-2.0
 *
 * Copyright (C) 2017-2018 Jason A. Donenfeld <Jason@zx2c4.com>. All Rights Reserved.
 */

package rwcancel

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
	"syscall"
)

type RWCancel struct {
	fd            int
	closingReader *os.File
	closingWriter *os.File
}

type fdSet struct {
	fdset unix.FdSet
}

func (fdset *fdSet) set(i int) {
	bits := 32 << (^uint(0) >> 63)
	fdset.fdset.Bits[i/bits] |= 1 << uint(i%bits)
}

func (fdset *fdSet) check(i int) bool {
	bits := 32 << (^uint(0) >> 63)
	return (fdset.fdset.Bits[i/bits] & (1 << uint(i%bits))) != 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func NewRWCancel(fd int) (*RWCancel, error) {
	err := unix.SetNonblock(fd, true)
	if err != nil {
		return nil, err
	}
	rwcancel := RWCancel{fd: fd}

	rwcancel.closingReader, rwcancel.closingWriter, err = os.Pipe()
	if err != nil {
		return nil, err
	}

	runtime.SetFinalizer(&rwcancel, func(rw *RWCancel) {
		rw.Cancel()
	})

	return &rwcancel, nil
}

/* https://golang.org/src/crypto/rand/eagain.go */
func ErrorIsEAGAIN(err error) bool {
	if pe, ok := err.(*os.PathError); ok {
		if errno, ok := pe.Err.(syscall.Errno); ok && errno == syscall.EAGAIN {
			return true
		}
	}
	if errno, ok := err.(syscall.Errno); ok && errno == syscall.EAGAIN {
		return true
	}
	return false
}

func (rw *RWCancel) ReadyRead() bool {
	closeFd := int(rw.closingReader.Fd())
	fdset := fdSet{}
	fdset.set(rw.fd)
	fdset.set(closeFd)
	err := unixSelect(max(rw.fd, closeFd)+1, &fdset.fdset, nil, nil, nil)
	if err != nil {
		return false
	}
	if fdset.check(closeFd) {
		return false
	}
	return fdset.check(rw.fd)
}

func (rw *RWCancel) ReadyWrite() bool {
	closeFd := int(rw.closingReader.Fd())
	fdset := fdSet{}
	fdset.set(rw.fd)
	fdset.set(closeFd)
	err := unixSelect(max(rw.fd, closeFd)+1, nil, &fdset.fdset, nil, nil)
	if err != nil {
		return false
	}
	if fdset.check(closeFd) {
		return false
	}
	return fdset.check(rw.fd)
}

func (rw *RWCancel) Read(p []byte) (n int, err error) {
	for {
		n, err := unix.Read(rw.fd, p)
		if err == nil || !ErrorIsEAGAIN(err) {
			return n, err
		}
		if !rw.ReadyRead() {
			return 0, errors.New("fd closed")
		}
	}
}

func (rw *RWCancel) Write(p []byte) (n int, err error) {
	for {
		n, err := unix.Write(rw.fd, p)
		if err == nil || !ErrorIsEAGAIN(err) {
			return n, err
		}
		if !rw.ReadyWrite() {
			return 0, errors.New("fd closed")
		}
	}
}

func (rw *RWCancel) Cancel() (err error) {
	_, err = rw.closingWriter.Write([]byte{0})
	return
}
