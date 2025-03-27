//
//  MIT License
//
//  (C) Copyright 2023-2024 Hewlett Packard Enterprise Development LP
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

// File contains communication with the console-operator
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
)

type OperatorService interface {
	getPodLocation(podId string) (podLoc *PodLocationDataResponse, err error)
	OperatorRetryInterval() time.Duration
}

type OperatorManager struct {
	operatorAddrBase      string
	operatorRetryInterval time.Duration
}

func NewOperatorService() *OperatorManager {
	var operatorRetryInterval time.Duration = time.Duration(30 * float64(time.Second))
	return &OperatorManager{
		operatorAddrBase:      "http://cray-console-operator/console-operator/v1",
		operatorRetryInterval: operatorRetryInterval,
	}
}

func (om OperatorManager) OperatorRetryInterval() time.Duration {
	return om.operatorRetryInterval
}

type PodLocationDataResponse struct {
	PodName string `json:"podname"`
	Alias   string `json:"alias"`
	Xname   string `json:"xname"`
}

func (om OperatorManager) getPodLocation(podID string) (data *PodLocationDataResponse, err error) {
	log.Printf("Getting pod location from console-operator for pod %s\n", podID)
	url := fmt.Sprintf("%s/location/%s", om.operatorAddrBase, podID)
	rb, sc, err := getURL(url, nil)
	if err != nil {
		log.Printf("Error making GET to %s\n", url)
		return nil, err
	}

	if sc != 200 && err == nil {
		return nil, errors.New("failed to getPodLocation")
	}

	var resp = new(PodLocationDataResponse)
	if rb != nil {
		err := json.Unmarshal(rb, &resp)
		if err != nil {
			log.Printf("Error unmarshalling return data: %s\n", err)
			return nil, err
		}
	}

	return resp, nil
}
