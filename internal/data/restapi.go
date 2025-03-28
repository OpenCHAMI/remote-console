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

// This file contains REST API implementations.

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
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

type nodeConsoleInfoHeartBeat struct {
	CurrNodes   []NodeConsoleInfo
	PodLocation string // location of the current node pod in kubernetes
}

// Struct to hold information about currently active node pods
type NodePodInfo struct {
	NumActivePods int `json:"numactivepods"`
}

func newNCI(nodeName, bmcName, bmcFqdn, class, role string, nid int) NodeConsoleInfo {
	return NodeConsoleInfo{NodeName: nodeName, BmcName: bmcName, BmcFqdn: bmcFqdn,
		Class: class, NID: nid, Role: role}
}

// acquireNodes(podId, numRiver, numMtn) → returns list of nodes and assigns them to pod with current timestamp (called by console-node)
// console-node will also provide the node alias and xname it is running on to filter for resiliency purposes.
// pod_id will be stateful set named (node-1, node-1, node-x)
// Give me up to 1k mtn and 500 river.
// Makes the assignments based on what is available.
// Return the new list of nodes (consoleNI struct) of what was assigned.
// May return nothing in the vast majority of times.
func consolePodAcquireNodes(w http.ResponseWriter, r *http.Request) {
	type ReqData struct {
		NumMtn int    `json:"nummtn"` // Requested number of Mountain nodes
		NumRvr int    `json:"numrvr"` // Requested number of River nodes
		Xname  string `json:"xname"`  // Xname of current node pod is running on
		Alias  string `json:"alias"`  // Alias of current node pod is running on
	}

	pod_id := getField(r, 0)
	if pod_id == "" {
		log.Printf("Missing console pod_id.\n")
		var body = BaseResponse{
			Msg: fmt.Sprintf("Missing console pod_id"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("consolePodAcquireNodes pod_id=%s\n", pod_id)

	reqBody, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("There was an error reading the request body: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error reading the request body: S%s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	contentType := r.Header.Get("Content-type")
	log.Printf("Content-Type: %s\n", contentType)
	if contentType != "application/json" {
		var body = BaseResponse{
			Msg: fmt.Sprintf("Expecting Content-Type: application/json"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("request data: %s\n", string(reqBody))
	var reqData ReqData
	err = json.Unmarshal(reqBody, &reqData)
	if err != nil {
		log.Printf("There was an error while decoding the json data: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while decoding the json data: %s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}

	_, ncisAcquired, err := dbConsolePodAcquireNodes(
		pod_id,
		reqData.NumMtn,
		reqData.NumRvr,
	)

	if err != nil {
		log.Printf("There was an error while acquiring nodes: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while acquiring nodes: %s", err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return

	}

	SendResponseJSON(w, http.StatusOK, ncisAcquired)
}

/*
heartbeat(podId, podNodes[]) → returns list of nodes not assigned to this pod any more, updates
timestamp of valid nodes (called by console-node)
*/
func consolePodHeartbeat(w http.ResponseWriter, r *http.Request) {
	pod_id := getField(r, 0)
	log.Printf("consolePodHeartbeat pod_id=%s\n", pod_id)
	if pod_id == "" {
		log.Printf("Missing console pod_id.\n")
		var body = BaseResponse{
			Msg: fmt.Sprintf("Missing console pod_id"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}

	reqBody, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("There was an error reading the request body: S%s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error reading the request body: S%s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	contentType := r.Header.Get("Content-type")
	log.Printf("Content-Type: %s\n", contentType)
	if contentType != "application/json" {
		var body = BaseResponse{
			Msg: fmt.Sprintf("Expecting Content-Type: application/json"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("request data: %s\n", string(reqBody))

	var heartBeatResponse nodeConsoleInfoHeartBeat
	err = json.Unmarshal(reqBody, &heartBeatResponse)
	log.Printf("heartBeatResponse: %+v\n", heartBeatResponse)

	if err != nil {
		log.Printf("There was an error while decoding the json data: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while decoding the json data: %s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	_, notUpdated, err := dbConsolePodHeartbeat(pod_id, &heartBeatResponse)
	if err != nil {
		log.Printf("There was an error while trying to update heartbeat data for console pod %s.  Error: %s\n", pod_id, err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while trying to update heartbeat data for console pod %s.  Error: %s", pod_id, err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return
	}
	SendResponseJSON(w, http.StatusOK, notUpdated)
}

/*
findPod(node) → returns pod id of console pod that is monitoring that node (called by console-operator)
*/
func findConsolePodForNode(w http.ResponseWriter, r *http.Request) {
	xname := getField(r, 0)
	if xname == "" {
		log.Println("Missing xname.")
		var body = BaseResponse{
			Msg: fmt.Sprintf("Missing xname."),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}

	log.Printf("findConsolePodForNode xname=%s\n", xname)

	var nci NodeConsoleInfo
	nci.NodeName = xname

	err := dbFindConsolePodForNode(&nci)
	if err != nil {
		log.Printf("There was an error while trying to find the console pod (Node: %s).  Error: %s\n", xname, err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while trying to find the console pod (Node: %s).  Error: %s", xname, err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return
	}

	if nci.NodeConsoleName == "" {
		// Let the caller know that we did not find a console pod
		// for the given node.
		SendResponseJSON(w, http.StatusNotFound, nci)
		return
	}

	// Let the caller know we were successful.  The console pod
	// is part of the response in nci.
	SendResponseJSON(w, http.StatusOK, nci)
	return
}

/*
updateNodes(allNodes[]) → ensure there is an entry for all nodes in the input list - create new entry
where needed (called by console-operator)
*/
func updateNodes(w http.ResponseWriter, r *http.Request) {
	log.Printf("updateNodes\n")
	reqBody, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("There was an error reading the request body: S%s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error reading the request body: S%s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	contentType := r.Header.Get("Content-type")
	log.Printf("Content-Type: %s\n", contentType)
	if contentType != "application/json" {
		var body = BaseResponse{
			Msg: fmt.Sprintf("Expecting Content-Type: application/json"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("request data: %s\n", string(reqBody))
	var reqData []NodeConsoleInfo
	err = json.Unmarshal(reqBody, &reqData)
	if err != nil {
		log.Printf("There was an error while decoding the json data: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while decoding the json data: %s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	rowsInserted, err := dbUpdateNodes(&reqData)
	if err != nil {
		log.Printf("There was an error while updating nodes: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while acquiring nodes: %s", err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return
	}

	if rowsInserted > 0 {
		// Tell the caller that we actually created some records.
		var body = BaseResponse{
			Msg: fmt.Sprintf("created=%d", rowsInserted),
		}
		SendResponseJSON(w, http.StatusCreated, body)
	} else {
		// We were successful but there were no records to create.
		SendResponseJSON(w, http.StatusOK, nil)
	}

}

// clearStaleNodes(timestamp) → looks for HSM nodes with timestamp older than the given duration (in minutes) and
// clears pod info (called by console-operator) for each stale node
// Remove the pod entry from the liveness table
// update the ownership table setting unsetting the conman-pod-id where conman-pod-id = state pod name
func clearStaleNodes(w http.ResponseWriter, r *http.Request) {
	durationStr := getField(r, 0) // Duration in minutes
	if durationStr == "" {
		log.Printf("Missing duration.\n")
		var body = BaseResponse{
			Msg: fmt.Sprintf("Missing duration."),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("clearStaleNodes duration=%s\n", durationStr)
	duration, err := strconv.Atoi(durationStr)
	if err != nil {
		log.Printf("%s is not a valid duration.\n", durationStr)
		var body = BaseResponse{
			Msg: fmt.Sprintf("%s is not a valid duration.", durationStr),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}

	rowsAffected, err := dbClearStaleNodes(duration)
	if err != nil {
		log.Printf("There was an error while clearing console pod info (duration was: %d)  Error: %s\n", duration, err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while clearing console pod info (duration was: %d)  Error: %s", duration, err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return
	}

	if rowsAffected > 0 {
		// Tell the caller that we actually updated some records.
		var body = BaseResponse{
			Msg: fmt.Sprintf("updated=%d", rowsAffected),
		}
		SendResponseJSON(w, http.StatusNoContent, body)
	} else {
		SendResponseJSON(w, http.StatusOK, nil)
	}
}

// consolePodRelease -> takes []NodeConsoleInfo, pod no longer monitors these nodes, free for acquisition
// update the ownership table setting the conman-pod-id to NULL where node_name in ( nci.NodeName[,nci.NodeName]... )
func consolePodRelease(w http.ResponseWriter, r *http.Request) {
	pod_id := getField(r, 0)
	log.Printf("consolePodRelease pod_id=%s\n", pod_id)
	if pod_id == "" {
		log.Printf("Missing console pod_id.\n")
		var body = BaseResponse{
			Msg: fmt.Sprintf("Missing console pod_id"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}

	reqBody, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("There was an error reading the request body: S%s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error reading the request body: S%s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	contentType := r.Header.Get("Content-type")
	log.Printf("Content-Type: %s\n", contentType)
	if contentType != "application/json" {
		var body = BaseResponse{
			Msg: fmt.Sprintf("Expecting Content-Type: application/json"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("request data: %s\n", string(reqBody))

	var ncis []NodeConsoleInfo
	err = json.Unmarshal(reqBody, &ncis)
	if err != nil {
		log.Printf("There was an error while decoding the json data: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while decoding the json data: %s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}

	rowsUpdated, err := dbConsolePodRelease(pod_id, &ncis)
	if err != nil {
		log.Printf("There was an error while trying to release ownership for console pod %s.  Error: %s\n", pod_id, err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while trying to release ownership for console pod %s.  Error: %s", pod_id, err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return
	}

	// Tell the caller that we were successful and the count (if any).
	var body = BaseResponse{
		Msg: fmt.Sprintf("deleted=%d", rowsUpdated),
	}
	SendResponseJSON(w, http.StatusOK, body)
}

// deleteNodes -> takes []NodeConsoleInfo, - these nodes are no longer in the system at all
// delete from ownership where node_name in ( nci.NodeName[,nci.NodeName]... )
func deleteNodes(w http.ResponseWriter, r *http.Request) {

	log.Printf("deleteNodes\n")
	reqBody, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("There was an error reading the request body: S%s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error reading the request body: S%s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	contentType := r.Header.Get("Content-type")
	log.Printf("Content-Type: %s\n", contentType)
	if contentType != "application/json" {
		var body = BaseResponse{
			Msg: fmt.Sprintf("Expecting Content-Type: application/json"),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	log.Printf("request data: %s\n", string(reqBody))
	var reqData []NodeConsoleInfo
	err = json.Unmarshal(reqBody, &reqData)
	if err != nil {
		log.Printf("There was an error while decoding the json data: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while decoding the json data: %s", err),
		}
		SendResponseJSON(w, http.StatusBadRequest, body)
		return
	}
	rowsDeleted, err := dbDeleteNodes(&reqData)
	//log.Printf("rowsDeleted: %d", rowsDeleted)
	if err != nil {
		log.Printf("There was an error while deleting nodes: %s\n", err)
		var body = BaseResponse{
			Msg: fmt.Sprintf("There was an error while deleting nodes: %s", err),
		}
		SendResponseJSON(w, http.StatusInternalServerError, body)
		return
	}

	// Tell the caller that we were successful and the delete count (if any).
	var body = BaseResponse{
		Msg: fmt.Sprintf("deleted=%d", rowsDeleted),
	}
	SendResponseJSON(w, http.StatusOK, body)
}

// Basic liveness probe
func getNumActiveNodePods(w http.ResponseWriter, r *http.Request) {
	// Query the database for the number of currently active pods
	var npi NodePodInfo
	npi.NumActivePods = dbFindActiveConsolePods()

	// Let the caller know we were successful.  The console pod
	// is part of the response in nci.
	SendResponseJSON(w, http.StatusOK, npi)
	return
}

// Basic liveness probe
func doLiveness(w http.ResponseWriter, r *http.Request) {
	// NOTE: this is coded in accordance with kubernetes best practices
	//  for liveness/readiness checks.  This function should only be
	//  used to indicate the server is still alive and processing requests.

	// return simple StatusNoContent response to indicate server is alive
	w.WriteHeader(http.StatusNoContent)
	return
}

// Basic readiness probe
func doReadiness(w http.ResponseWriter, r *http.Request) {
	// NOTE: this is coded in accordance with kubernetes best practices
	//  for liveness/readiness checks.  This function should only be
	//  used to indicate the server is still alive and processing requests.

	// return simple StatusNoContent response to indicate server is alive
	w.WriteHeader(http.StatusNoContent)
	return
}
