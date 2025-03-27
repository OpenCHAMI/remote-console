//
//  MIT License
//
//  (C) Copyright 2021-2022 Hewlett Packard Enterprise Development LP
//
//  Permission is hereby granted, free of charge, to any person obtaining a
//  copy of this software and associated documentation files (the "Software"),
//  to deal in the Software without restriction, including without limitation
//  the rights to use, copy, modify, merge, publish, distribute, sublicense,
//  and/or sell copies of the Software, and to permit persons to whom the
//  Software is furnished to do so, subject to the following conditions:
//
//  The above copyright notice and this permission notice shall be included
//  in all copies or substantial portions of the Software.
//
//  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
//  THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR
//  OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
//  ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
//  OTHER DEALINGS IN THE SOFTWARE.
//

// This file contains assert implementations.

package main

import (
	"errors"
	"fmt"
	"runtime"
)

// Return a formatted error if expected != actual
func assertEqualInt(expected int, actual int, message string) (err error) {
	if expected != actual {
		_, fn, line, _ := runtime.Caller(1) // the file and line number of the caller
		if message != "" {
			msg := fmt.Sprintf("assertEqual failed [%s:%d]: %d expected, actual: %d - %s",
				fn, line, expected, actual, message)
			return errors.New(msg)
		} else {
			msg := fmt.Sprintf("assertEqual failed [%s:%d]: %d expected, actual: %d",
				fn, line, expected, actual)
			return errors.New(msg)
		}
	}
	return nil
}

// Return a formatted error if expected != actual
func assertEqualStr(expected string, actual string, message string) (err error) {
	if expected != actual {
		_, fn, line, _ := runtime.Caller(1) // the file and line number of the caller
		if message != "" {
			msg := fmt.Sprintf("assertEqual failed [%s:%d]: %s expected, actual: %s - %s",
				fn, line, expected, actual, message)
			return errors.New(msg)
		} else {
			msg := fmt.Sprintf("assertEqual failed [%s:%d]: %s expected, actual: %s",
				fn, line, expected, actual)
			return errors.New(msg)
		}
	}
	return nil
}
