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

// This file handles command line entry, test definition and test execution.

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
)

// Test name -> test function name
// (Add your new test here...)
var tests = map[string]interface{}{
	"inventoryCreate":       inventoryCreate,
	"inventoryCreateVolume": inventoryCreateVolume,
	"consolePodAcquire":     consolePodAcquire,
}

// Test metrics
var total_tests int = len(tests)
var total_pass int = 0
var total_fail int = 0
var testSummary = []string{}

// Record test failure.
func fail(testName string, err error) {
	msg := fmt.Sprintf("FAIL - %s: %s", testName, err)
	//log.Printf(msg)
	testSummary = append(testSummary, msg)
	total_fail++
}

// Record test passing.
func pass(testName string) {
	msg := fmt.Sprintf("PASS - %s", testName)
	//log.Printf(msg)
	testSummary = append(testSummary, msg)
	total_pass++
}

// Test summary
func summary() {
	log.Printf("---- TEST SUMMARY ----")
	for _, test := range testSummary {
		log.Printf(test)
	}
	msg := fmt.Sprintf("Total: %d   Pass: %d   Fail: %d", total_tests, total_pass, total_fail)
	log.Printf(msg)
}

// Use reflection to call the correct test function passing in the context.
func call(funcName string, params ...interface{}) (result interface{}, err error) {
	f := reflect.ValueOf(tests[funcName])
	if len(params) != f.Type().NumIn() {
		err = errors.New("The number of params is out of index.")
		return
	}
	in := make([]reflect.Value, len(params))
	for k, param := range params {
		in[k] = reflect.ValueOf(param)
	}
	f.Call(in)
	return
}

// The main test runner.  Loop the list of configured
// tests calling each.
func testMain() (rc int) {
	log.Printf("---- START INTEGRATION TESTS ----")
	for testName := range tests {
		call(testName, testName)
	}
	summary()
	return total_fail
}

// Main entry.  Exits with the overall test status.
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	os.Exit(testMain())
}
