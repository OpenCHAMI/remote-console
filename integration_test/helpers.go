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

// This file contains helper methods to support test verification.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq" //needed for DB stuff
)

// Datastore is a collection of db-centric helper methods for use as needed
// to assist in testing by providing db operations not included in the API.
type DataStore struct {
	DB *sql.DB
}

// Init initializes the db connection and waits for a basic check to complete.
func (ds *DataStore) Init() {
	// Get environment var with default.
	getEnv := func(key, fallback string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		return fallback
	}

	// Rely on these hardcoded values for now until they can get properly plumbed
	// into the compose environment.
	dbUserName := getEnv("POSTGRES_USER", "console")
	dbName := getEnv("POSTGRES_DB", "console")
	dbHostName := getEnv("POSTGRES_HOST", "integtest-condat-postgres-1") // POSTGRES_HOST
	dbPort := getEnv("POSTGRES_PORT", "5432")
	dbPasswd := getEnv("POSTGRES_PASSWD", "console")

	connStr := fmt.Sprintf("sslmode=disable user=%s dbname=%s host=%s port=%s", dbUserName, dbName,
		dbHostName, dbPort)

	log.Printf("Attempt to open DB conn as: %s", connStr)
	connStr += " password=" + dbPasswd
	log.Printf(connStr)
	var err error
	ds.DB, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Panicf("Unable to open DB connection: %s", err)
	}
	log.Printf("Opened DB conn")

	retries := 6 //wait for 10 seconds for the network to come up,
	//then fail
	// Try an operation
	for err := ds.CheckConn(); err != nil; err = ds.CheckConn() {
		retries -= 1
		if retries == 0 {
			log.Fatalln("couldn't connect to db host:", dbHostName)
		}
		log.Printf("Waiting on db connection")
		time.Sleep(2 * time.Second)

	}
	log.Printf("db connected")

}

// Close closees the db connection.
func (ds *DataStore) Close() {
	if ds.DB != nil {
		ds.DB.Close()
	}
}

// RemoveAll deletes all items from the console ownership.
func (ds *DataStore) RemoveAll() (rowsAffected int64, err error) {
	sqlStmt := `
		delete from ownership
	`
	result, err := ds.DB.Exec(sqlStmt)
	rowsAffected = 0
	if err != nil {
		errMsg := fmt.Sprintf("WARN: RemoveAll: There is a DELETE error: %s", err)
		log.Printf(errMsg)
		err = errors.New(errMsg)
	}
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
		log.Printf("result.RowsAffected %d", rowsAffected)
	}
	return rowsAffected, err
}

// CheckConn will try to execute a query
func (ds *DataStore) CheckConn() (err error) {
	sqlStmt := `
		select count(1) from ownership
	`
	_, err = ds.DB.Exec(sqlStmt)
	if err != nil {
		errMsg := fmt.Sprintf("WARN: GetCount: There is a SELECT error: %s", err)
		log.Printf(errMsg)
		err = errors.New(errMsg)
		return err
	}
	return nil
}

// GetCount returns the number of records in console ownership.
func (ds *DataStore) GetCount() (recordCount int64, err error) {
	sqlStmt := `
		select count(1) from ownership
	`
	result, err := ds.DB.Query(sqlStmt)
	if err != nil {
		errMsg := fmt.Sprintf("WARN: GetCount: There is a SELECT error: %s", err)
		log.Printf(errMsg)
		err = errors.New(errMsg)
		return 0, err
	}
	defer result.Close()
	recordCount = 0
	if result == nil {
		errMsg := fmt.Sprintf("WARN: GetCount: Error getting the result: %s", err)
		log.Printf(errMsg)
		err = errors.New(errMsg)
		return 0, err
	}
	if !result.Next() {
		errMsg := fmt.Sprintf("WARN: GetCount: missing count")
		log.Printf(errMsg)
		err = errors.New(errMsg)
		return 0, err
	}
	err = result.Scan(&recordCount)
	if err != nil {
		errMsg := fmt.Sprintf("WARN: GetCount: Error getting the count: %s", err)
		log.Printf(errMsg)
		err = errors.New(errMsg)
		return 0, err
	}
	//log.Printf("count: %d", recordCount)
	return recordCount, err
}
