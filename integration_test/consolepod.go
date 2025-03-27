//
//  MIT License
//
//  (C) Copyright 2021-2023 Hewlett Packard Enterprise Development LP
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

// This file contains consolepod API specific tests.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

// consolePodAcquire will verify acquisition of nodes by
// the requested console pod.
func consolePodAcquire(testName string) {

	// Wipe any previous records
	_, err := clearInventory()
	if err != nil {
		fail(testName, err)
		return
	}

	// Create some mock inventory data
	var ncis = []NodeConsoleInfo{}
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("x3000c0s%db0n0", i) // sure, this looks good ...
		ncis = append(ncis, nci(name, name, name, "River", 100+i, "Compute", ""))
	}
	for i := 25; i < 50; i++ {
		name := fmt.Sprintf("x3000c0s%db0n0", i) // sure, this looks good ...
		ncis = append(ncis, nci(name, name, name, "Mountain", 100+i, "Compute", ""))
	}

	// Create the records now
	aop := InventoryApiOp{}
	recordCount, statusCode, responseBodyMessage, err := aop.Put(ncis)
	log.Printf("inventoryCreateByCount returning %d , %d , %s , %s\n", recordCount, statusCode, responseBodyMessage, err)

	if err != nil {
		fail(testName, err)
		return
	}

	err = assertEqualInt(50, int(recordCount), "")
	if err != nil {
		fail(testName, err)
		return
	}

	// Prepare to acquire nodes
	caop := ConsoleApiOp{}
	statusCode, ncisAcquired, err := caop.Acquire("pod1", 5, 5)
	if err != nil {
		fail(testName, err)
		return
	}

	err = assertEqualInt(10, len(ncisAcquired), "")
	if err != nil {
		fail(testName, err)
	}

	err = assertEqualInt(http.StatusOK, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	pass(testName)

}

// ConsoleApiOp performs repetitive API tasks
type ConsoleApiOp struct {
	ReqData struct {
		NumMtn int `json:"nummtn"` // Requested number of Mountain nodes
		NumRvr int `json:"numrvr"` // Requested number of River nodes
	}
	respBody struct {
		Message string `json:"message"`
	}
}

// Acquire takes a console pod and the number of nodes to acquire.
// Returns:
// statusCode - the http response code
// ncisAcquired - the list of nodes acquired
// err - any error
func (o *ConsoleApiOp) Acquire(console_pod_id string, numMtn, numRvr int) (statusCode int, ncisAcquired []NodeConsoleInfo, err error) {

	if console_pod_id == "" {
		return 0, nil, errors.New("console_pod_id is required but was empty")
	}

	uri := "http://cray-console-data/v1/consolepod/%s/acquire"
	uri = fmt.Sprintf(uri, console_pod_id)
	client := &http.Client{Timeout: 15 * time.Second}
	o.ReqData.NumMtn = numMtn
	o.ReqData.NumRvr = numRvr

	//log.Println("InventoryApiOp.Put() called")
	jsonReq, err := json.Marshal(o.ReqData)
	if err != nil {
		return 0, nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, uri, bytes.NewBuffer(jsonReq))
	if err != nil {
		return 0, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Send the request
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	statusCode = httpResp.StatusCode
	//log.Printf("statusCode=%d", statusCode)

	ncisAcquired = []NodeConsoleInfo{}
	json.NewDecoder(httpResp.Body).Decode(&ncisAcquired)
	defer httpResp.Body.Close()

	// Return everything to the caller for evaluation.
	return statusCode, ncisAcquired, nil
}
