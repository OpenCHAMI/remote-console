//
//  MIT License
//
//  (C) Copyright 2019-2022, 2024 Hewlett Packard Enterprise Development LP
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

// This file contains helper functions for http interactions

package console

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
)

// ErrResponse - Simple struct to return error information
type ErrResponse struct {
	E      int    `json:"e"` // Error code
	ErrMsg string `json:"err_msg"`
}

// Send error or empty OK response
func sendJSONError(w http.ResponseWriter, ecode int, message string) {
	// If HTTP call is success, put zero in returned json error field.
	httpCode := ecode
	if ecode >= 200 && ecode <= 299 {
		ecode = 0
	}

	data := ErrResponse{
		E:      ecode,
		ErrMsg: message,
	}

	SendResponseJSON(w, httpCode, data)
}

// SendResponseJSON sends data marshalled as a JSON body and sets the HTTP
// status code to sc.
func SendResponseJSON(w http.ResponseWriter, sc int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(sc)

	err := json.NewEncoder(w).Encode(data)
	if err != nil {
		log.Printf("Error: encoding/sending JSON response: %s\n", err)
		return
	}
}

// Helper function to execute an http POST command
func postURL(URL string, requestBody []byte, requestHeaders map[string]string) ([]byte, int, error) {
	var err error = nil
	//log.Printf("postURL URL: %s\n", URL)
	req, err := http.NewRequest("POST", URL, bytes.NewReader(requestBody))
	if err != nil {
		// handle error
		log.Printf("postURL Error creating new request to %s: %s", URL, err)
		return nil, -1, err
	}
	req.Header.Add("Content-Type", "application/json")
	if requestHeaders != nil {
		for k, v := range requestHeaders {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		// Always drain and close response bodies, just in case
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		// handle error
		log.Printf("postURL Error on request to %s: %s", URL, err)
		return nil, -1, err
	}

	//log.Printf("postURL Response Status code: %d\n", resp.StatusCode)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		// handle error
		log.Printf("postURL Error reading response: %s", err)
		return nil, resp.StatusCode, err
	}
	//fmt.Printf("Data: %s\n", data)
	return data, resp.StatusCode, err
}

// Helper function to execute an http command
func getURL(URL string, requestHeaders map[string]string) ([]byte, int, error) {
	var err error = nil
	//log.Printf("getURL URL: %s\n", URL)
	req, err := http.NewRequest("GET", URL, nil)
	if err != nil {
		// handle error
		log.Printf("getURL Error creating new request to %s: %s", URL, err)
		return nil, -1, err
	}
	if requestHeaders != nil {
		for k, v := range requestHeaders {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		// Always drain and close response bodies, just in case
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		// handle error
		log.Printf("getURL Error on request to %s: %s", URL, err)
		return nil, -1, err
	}
	defer resp.Body.Close()
	//log.Printf("getURL Response Status code: %d\n", resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		// handle error
		log.Printf("Error reading response: %s", err)
		return nil, resp.StatusCode, err
	}
	// NOTE: Dumping entire response clogs up the log file but keep for debugging
	//fmt.Printf("Data: %s\n", data)
	return data, resp.StatusCode, err
}

// Utility function to ensure that a directory exists
func EnsureDirPresent(dir string, perm os.FileMode) (bool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Printf("Directory does not exist, creating: %s", dir)
		err = os.MkdirAll(dir, perm)
		if err != nil {
			log.Printf("Unable to create dir: %s", err)
			return false, err
		}
	}
	return true, nil
}
