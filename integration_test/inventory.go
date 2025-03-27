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

// This file contains inventory API specific tests.

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

// Struct to hold all node level information needed to form a console connection
type NodeConsoleInfo struct {
	NodeName        string `json:"nodename"`        // node xname
	BmcName         string `json:"bmcname"`         // bmc xname
	BmcFqdn         string `json:"bmcfqdn"`         // full name of bmc
	Class           string `json:"class"`           // river/mtn class
	NID             int    `json:"nid"`             // NID of the node
	Role            string `json:"role"`            // role of the node
	NodeConsoleName string `json:"nodeconsolename"` // the pod console
}

func nci(nodeName string, bmcName string, bmcFqdn string, class string,
	nid int, role string, consoleName string) NodeConsoleInfo {

	return NodeConsoleInfo{
		nodeName,
		bmcName,
		bmcFqdn,
		class,
		nid,
		role,
		consoleName}
}

// inventoryCreate focus on issuing multiple requests some with repeated data
// and confirmation that the number or items created is as expected.
func inventoryCreate(testName string) {
	// Wipe any previous records
	_, err := clearInventory()
	if err != nil {
		fail(testName, err)
	}

	var ncis = []NodeConsoleInfo{}
	ncis = append(ncis, nci("x3000c0s1b0n0", "x3000c0s1b0", "x3000c0s1b0", "River", 100001, "Compute", ""))
	ncis = append(ncis, nci("x3000c0s2b0n0", "x3000c0s2b0", "x3000c0s2b0", "River", 100002, "Compute", ""))

	aop := InventoryApiOp{}
	recordCount, statusCode, responseBodyMessage, err := aop.Put(ncis)

	// Expect 2 new records created
	if err != nil {
		fail(testName, err)
	}
	// Total record count
	err = assertEqualInt(2, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	// Number created by this request
	err = assertEqualStr("created=2", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 201 status is expected.
	err = assertEqualInt(201, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	ncis = append(ncis, nci("x3000c0s3b0n0", "x3000c0s3b0", "x3000c0s3b0", "Mountain", 100003, "Compute", ""))
	ncis = append(ncis, nci("x3000c0s4b0n0", "x3000c0s4b0", "x3000c0s4b0", "Mountain", 100004, "Compute", ""))
	ncis = append(ncis, nci("x3000c0s5b0n0", "x3000c0s5b0", "x3000c0s5b0", "Mountain", 100005, "Compute", ""))

	aop = InventoryApiOp{}
	recordCount, statusCode, responseBodyMessage, err = aop.Put(ncis)

	// Expect 3 new records created
	if err != nil {
		fail(testName, err)
	}
	// Total record count
	err = assertEqualInt(2+3, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	// Number created by this request
	err = assertEqualStr("created=3", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 201 status is expected.
	err = assertEqualInt(201, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	// Repeat the previous request and expect no new records created
	aop = InventoryApiOp{}
	recordCount, statusCode, responseBodyMessage, err = aop.Put(ncis)
	err = assertEqualInt(2+3, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	// Number created by this request
	err = assertEqualStr("", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 200 status is expected.
	err = assertEqualInt(200, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	pass(testName)
}

// inventoryCreateVolume focus is on creating larger blocks of records and confirmation of success
func inventoryCreateVolume(testName string) {
	// Wipe any previous records
	_, err := clearInventory()
	if err != nil {
		fail(testName, err)
	}
	// Request new records be created
	recordCount, statusCode, responseBodyMessage, err := inventoryCreateByCount(0, 2500)
	if err != nil {
		fail(testName, err)
	}
	// Trust but verify ..
	// Total record count
	err = assertEqualInt(2500, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	// Number created by this request
	err = assertEqualStr("created=2500", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 201 status is expected.
	err = assertEqualInt(201, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	// Request additional records be created
	recordCount, statusCode, responseBodyMessage, err = inventoryCreateByCount(2500, 500)
	if err != nil {
		fail(testName, err)
	}
	// Trust but verify ..
	err = assertEqualInt(2500+500, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	err = assertEqualStr("created=500", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 201 status is expected.
	err = assertEqualInt(201, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	// Request additional records be created
	recordCount, statusCode, responseBodyMessage, err = inventoryCreateByCount(3000, 5000)
	if err != nil {
		fail(testName, err)
	}
	// Trust but verify ..
	err = assertEqualInt(2500+500+5000, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	err = assertEqualStr("created=5000", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 201 status is expected.
	err = assertEqualInt(201, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	// Try to add duplicates.  This is accepted but no records are added.
	recordCount, statusCode, responseBodyMessage, err = inventoryCreateByCount(0, 500)
	if err != nil {
		fail(testName, err)
	}
	// The existing record count does not change.
	err = assertEqualInt(2500+500+5000, int(recordCount), "")
	if err != nil {
		fail(testName, err)
	}
	// No new records were added.
	err = assertEqualStr("", responseBodyMessage, "")
	if err != nil {
		fail(testName, err)
	}
	// 200 status is expected.
	err = assertEqualInt(200, statusCode, "")
	if err != nil {
		fail(testName, err)
	}

	// TODO: Add some checking of status code and message vs expected values
	// (and pass these in...)
	pass(testName)
}

// inventoryDelete verifies inverntory remove
func inventoryDelete(testName string) {

}

// inventoryCreate will attempt to create the number of inventory records requested.
// The host names are generated and deterministic.
func inventoryCreateByCount(startAt int, requested int) (recordCount int64, statusCode int, responseBodyMessage string, err error) {
	// Create some mock inventory data
	var ncis = []NodeConsoleInfo{}
	for i := startAt; i < startAt+requested; i++ {
		name := fmt.Sprintf("x3000c0s%db0n0", i) // sure, this looks good ...
		ncis = append(ncis, nci(name, name, name, "River", 100+i, "Compute", ""))
	}

	// Send the api request and return the results for evaluation by the caller.
	aop := InventoryApiOp{}
	recordCount, statusCode, responseBodyMessage, err = aop.Put(ncis)
	log.Printf("inventoryCreateByCount returning %d , %d , %s , %s\n", recordCount, statusCode, responseBodyMessage, err)
	return recordCount, statusCode, responseBodyMessage, err
}

// clearInventory is a test helper to remove all items from the inventory datastore
func clearInventory() (recordsRemoved int64, err error) {
	var ds DataStore
	ds.Init()
	defer ds.Close()
	recordsRemoved, err = ds.RemoveAll()
	return recordsRemoved, err
}

// InventoryApiOp performs repetitive API tasks
type InventoryApiOp struct {
	respBody struct {
		Message string `json:"message"`
	}
}

// Put takes a list of nodes and attempts to add the nodes to inventory.
// Returns:
// recordCount - the number of records in the underlying db
// statusCode - the http response code
// respBody.Message - the reply (message) from the API
// err - any error
func (o *InventoryApiOp) Put(ncis []NodeConsoleInfo) (recordCount int64,
	statusCode int, responseBodyMessage string, err error) {
	const uri = "http://cray-console-data/v1/inventory"
	client := &http.Client{Timeout: 15 * time.Second}

	//log.Println("InventoryApiOp.Put() called")
	if ncis == nil {
		return 0, 0, "", errors.New("NodeConsoleInfo is required but was nil.")
	}
	jsonReq, err := json.Marshal(ncis)
	if err != nil {
		return 0, 0, "", err
	}
	httpReq, err := http.NewRequest(http.MethodPut, "http://cray-console-data/v1/inventory", bytes.NewBuffer(jsonReq))
	if err != nil {
		return 0, 0, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Send the request
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return 0, 0, "", err
	}
	statusCode = httpResp.StatusCode
	//log.Printf("statusCode=%d", statusCode)

	// Peek at the response
	responseBodyMessage = ""
	json.NewDecoder(httpResp.Body).Decode(&o.respBody)
	defer httpResp.Body.Close()
	responseBodyMessage = o.respBody.Message
	//log.Printf("responseBodyMessage=%d", responseBodyMessage)

	// Peek under the hood to check the record count
	var ds DataStore
	ds.Init()
	defer ds.Close()
	recordCount, err = ds.GetCount()
	//log.Printf("recordsCreated=%d", recordCount)

	// Return everything to the caller for evaluation.
	log.Printf("InventoryApiOp.Put() returning %d , %d , %s , %s\n", recordCount, statusCode, responseBodyMessage, err)
	return recordCount, statusCode, responseBodyMessage, nil
}
