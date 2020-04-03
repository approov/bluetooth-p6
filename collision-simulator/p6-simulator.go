// Simple simulation of P6 collision events
//
// MIT License
//
// Copyright (c) 2020, Critical Blue Ltd.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files
// (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge,
// publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR
// ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH
// THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"strconv"
	"sync"
)

// number of bytes for the 256-bit secrets
const sSecretBytes = 32

// fixed number of slices to generate the 256-bit codes
const sSlicesPerFrame = 8

// number of go routines to be launched to perform the computation
const sNumGoRoutines = 8

// Config is the configuration for the simulation. We are modelling how many collisions we expect in an epoch (i.e. a day) based on a simple
// model of the amount of received code slices. Thus the number of collisions output will be per-epoch. We must multiply this by the number
// of epochs (i.e. days) we would typically expect.
type Config struct {
	NumberOfDisclosures     int       `json:"numberOfDisclosures"`     // number of simulated epoch disclosures
	NumberOfReceivers       int       `json:"numberOfReceivers"`       // number of simulated receivers
	ReceptionProfile        []float64 `json:"receptionProfile"`        // list of floats for proximate transmitter probabilities (first entry for none)
	ReceptionMaxBurstPeriod int       `json:"receptionMaxBurstPeriod"` // maximum contiguous frames associated with reception or not (chosen randomly up to this value)
	EpochLength             int       `json:"epochLength"`             // length of an epoch in terms of the number of frames
	MatchesNeeded           int       `json:"matchesNeeded"`           // number of matches needed in a cluster for a collision
	ClusterSlices           int       `json:"clusterSlices"`           // number of slices in a cluster in which sufficient matches are required
}

// ReceivedDuringSlice records the information received in a single slice period
type ReceivedDuringSlice struct {
	SliceCodes []uint32 // number of slice codes received in the slice
}

// ReceptionTrace traces all of the slice codes received by a particular receiver in an epoch
type ReceptionTrace struct {
	Stream []*ReceivedDuringSlice // received stream in each slice (or nil if nothing received)
}

// SliceCodeStream records the slice codes for a complete stream
type SliceCodeStream struct {
	SliceCodes []uint32 // stream of slice codes during an epoch
}

// total number of collisions seen
var sTotalCollisions int

// total number of full matches seen
var sTotalMatches int

// mutex for updating the count of collisions
var sUpdateMutex sync.Mutex

// Generates the slice codes associated with an epoch. This uses a simulation of the code generation algorithm to generate
// realistic code values.
//
// @param pEpochLength is the number of frames to simulate
// @return *SliceCodeStream for the epoch, or nil if there was an error
// @return error if there was a problem, nil otherwise
func genRandEpochSliceCodes(pEpochLength int) (*SliceCodeStream, error) {
	// generate the IEC (Initial Epoch Code)
	aIEC := make([]byte, sSecretBytes)
	_, aErr := rand.Read(aIEC)
	if aErr != nil {
		return nil, aErr
	}

	// generate the EPK (Epoch Progresson Key)
	aEPK := make([]byte, sSecretBytes)
	_, aErr = rand.Read(aEPK)
	if aErr != nil {
		return nil, aErr
	}

	// now generate random codes for the epoch, frame by frame
	aSliceCodeStream := &SliceCodeStream{
		SliceCodes: make([]uint32, pEpochLength*sSlicesPerFrame, pEpochLength*sSlicesPerFrame),
	}
	aFC := make([]byte, sSecretBytes)
	copy(aFC, aIEC)
	for aFrameNum := 0; aFrameNum < pEpochLength; aFrameNum++ {
		// generate the slice codes for this frame
		aBaseIndex := aFrameNum * sSlicesPerFrame
		for aSliceIndex := 0; aSliceIndex < sSlicesPerFrame; aSliceIndex++ {
			aByteIndex := aSliceIndex << 2
			var aSliceCode uint32
			aSliceCode = uint32(aFC[aByteIndex])
			aSliceCode += uint32(aFC[aByteIndex+1]) << 8
			aSliceCode += uint32(aFC[aByteIndex+2]) << 16
			aSliceCode += uint32(aFC[aByteIndex+3]) << 24
			aSliceCodeStream.SliceCodes[aBaseIndex+aSliceIndex] = aSliceCode
		}

		// progress the codes for the next frame
		aMac := hmac.New(sha256.New, aEPK)
		aMac.Write(aFC)
		aFC = aMac.Sum(nil)
	}
	return aSliceCodeStream, nil
}

// Generates a simulated receiver with the slice codes that were received in a period. An overall receiving profile and burst period
// defines the characteristics of reception.
//
// @param pEpochLength is the number of frames to simulate
// @param pReceptionProfile is an array of float providing probabilities for different numbers of proximate transmitters (0 entry for none)
// @param pReceptionMaxBurstPeriod is the maximum number of frames for a burst period of particlar activity (chosen randomly to that amount)
// @return *ReceptionTrace is the trace recorded by the simulated receiver, or nil if there was an error
// @return error if there was a problem, nil otherwise
func genSimulatedReceiver(pEpochLength int, pReceptionProfile []float64, pReceptionMaxBurstPeriod int) (*ReceptionTrace, error) {
	// create traces for the maximum number of contacts that we will see - note that this means we are always randomly choosing from the
	// same contacts during the epoch - but this not matter in terms of the simulation since all traces are randomized
	aMaxTransmitters := len(pReceptionProfile) - 1
	if aMaxTransmitters < 0 {
		return nil, errors.New("invalid reception profile")
	}
	aTransmitterSliceCodes := make([]*SliceCodeStream, aMaxTransmitters, aMaxTransmitters)
	for aTransIndex := 0; aTransIndex < aMaxTransmitters; aTransIndex++ {
		aSliceCodeStream, aErr := genRandEpochSliceCodes(pEpochLength)
		if aErr != nil {
			return nil, aErr
		}
		aTransmitterSliceCodes[aTransIndex] = aSliceCodeStream
	}

	// generate the simulated reception with bursts of contacts up to some maximum - each burst will have a random number of contacts or 0
	aReceptionTrace := &ReceptionTrace{
		Stream: make([]*ReceivedDuringSlice, pEpochLength*sSlicesPerFrame, pEpochLength*sSlicesPerFrame),
	}
	aCurrentProximateTransmitters := 0
	aNextDecisionSliceIndex := 0
	for aSliceIndex := 0; aSliceIndex < (pEpochLength * sSlicesPerFrame); aSliceIndex++ {
		// make a decision on the number of transmitters for the next period
		if aSliceIndex >= aNextDecisionSliceIndex {
			aNextDecisionSliceIndex = aSliceIndex + 1 + rand.Intn(pReceptionMaxBurstPeriod)
			aRandFloat := rand.Float64()
			for aNumProxTrans, aThreshold := range pReceptionProfile {
				if aRandFloat <= aThreshold {
					aCurrentProximateTransmitters = aNumProxTrans
					break
				}
			}
		}

		// build the required received codes given the number of transmitters
		var aReceivedDuringSlice *ReceivedDuringSlice
		if aCurrentProximateTransmitters != 0 {
			aReceivedDuringSlice = &ReceivedDuringSlice{
				SliceCodes: make([]uint32, aCurrentProximateTransmitters, aCurrentProximateTransmitters),
			}
			for aTransIndex := 0; aTransIndex < aCurrentProximateTransmitters; aTransIndex++ {
				aReceivedDuringSlice.SliceCodes[aTransIndex] = aTransmitterSliceCodes[aTransIndex].SliceCodes[aSliceIndex]
			}
		}
		aReceptionTrace.Stream[aSliceIndex] = aReceivedDuringSlice
	}
	return aReceptionTrace, nil
}

// Counts the number of collisions between a disclosure stream and a given pre-generated reception stream.
//
// @param pDisclosureStream is the code slice stream associated with the disclosure
// @param pReceptionTrace is the trace of slice codes for an individual receiver
// @param pMatchesNeeded is the match requirement in a cluster
// @param pClusterSlices is the number of slices to be included in a cluster
// @param pStartSliceIndex is the first slice index to check from
// @return bool true if the match requirement was met
// @return error if there was a problem, nil otherwise
func isClusterMatch(pDisclosureStream *SliceCodeStream, pReceptionTrace *ReceptionTrace, pMatchesNeeded int, pClusterSlices int, pStartSliceIndex int) (bool, error) {
	aEndIndex := pStartSliceIndex + pClusterSlices
	if aEndIndex > len(pDisclosureStream.SliceCodes) {
		aEndIndex = len(pDisclosureStream.SliceCodes)
	}
	aCollisions := 0
	for aSliceIndex := pStartSliceIndex; aSliceIndex < aEndIndex; aSliceIndex++ {
		aDiscSliceCode := pDisclosureStream.SliceCodes[aSliceIndex]
		aReceived := pReceptionTrace.Stream[aSliceIndex]
		if aReceived != nil {
			for aIndex := 0; aIndex < len(aReceived.SliceCodes); aIndex++ {
				if aReceived.SliceCodes[aIndex] == aDiscSliceCode {
					aCollisions++
				}
			}
		}
	}
	return aCollisions >= pMatchesNeeded, nil
}

// Counts the number of collisions and full matches between a disclosure stream and a given pre-generated reception stream.
//
// @param pDisclosureStream is the code slice stream associated with the disclosure
// @param pReceptionTrace is the trace of slice codes for an individual receiver
// @param pMatchesNeeded is the match requirement in a cluster
// @param pClusterSlices is the number of slices to be included in a cluster
// @return int of the number of collisions detected
// @return int of the number of matches detected
// @return error if there was a problem, nil otherwise
func countCollisions(pDisclosureStream *SliceCodeStream, pReceptionTrace *ReceptionTrace, pMatchesNeeded int, pClusterSlices int) (int, int, error) {
	aCollisions := 0
	aMatches := 0
	for aSliceIndex := 0; aSliceIndex < len(pDisclosureStream.SliceCodes); aSliceIndex++ {
		aDiscSliceCode := pDisclosureStream.SliceCodes[aSliceIndex]
		aReceived := pReceptionTrace.Stream[aSliceIndex]
		if aReceived != nil {
			for aIndex := 0; aIndex < len(aReceived.SliceCodes); aIndex++ {
				if aReceived.SliceCodes[aIndex] == aDiscSliceCode {
					aCollisions++
					aIsMatch, aErr := isClusterMatch(pDisclosureStream, pReceptionTrace, pMatchesNeeded, pClusterSlices, aSliceIndex)
					if aErr != nil {
						return 0, 0, aErr
					}
					if aIsMatch {
						aMatches++
					}
				}
			}
		}
	}
	return aCollisions, aMatches, nil
}

/// Simulates a number of disclosures and updates the total number of collisions seen.
//
// @param pReceivers []*ReceptionTrace is set of receivers being simulated (read-only)
// @param pGoIndex is the index of the Go routine
// @param pNumDisclosures is the number of disclosures to be simulated
// @param pMatchesNeeded is the match requirement in a cluster
// @param pClusterSlices is the number of slices to be included in a cluster
// @return error if there was a problem, nil otherwise
func simulateDisclosures(pReceivers []*ReceptionTrace, pGoIndex int, pNumDisclosures int, pEpochLength int, pMatchesNeeded int, pClusterSlices int) error {
	for aDiscIndex := 0; aDiscIndex < pNumDisclosures; aDiscIndex++ {
		// create a random trace for the disclosure
		aDisclosureStream, aErr := genRandEpochSliceCodes(pEpochLength)
		if aErr != nil {
			return aErr
		}

		// check for a collision in any receiver
		aAllCollisions := 0
		aAllMatches := 0
		for aRecIndex := 0; aRecIndex < len(pReceivers); aRecIndex++ {
			aCollisions, aMatches, aErr := countCollisions(aDisclosureStream, pReceivers[aRecIndex], pMatchesNeeded, pClusterSlices)
			if aErr != nil {
				return aErr
			}
			aAllCollisions += aCollisions
			aAllMatches += aMatches
		}

		// update the collision total
		sUpdateMutex.Lock()
		sTotalCollisions += aAllCollisions
		sTotalMatches += aAllMatches
		aTotalCollisions := sTotalCollisions
		aTotalMatches := sTotalMatches
		sUpdateMutex.Unlock()

		// show the progress
		fmt.Printf("routine %d, %d/%d, collisions %d, matches %d\n", pGoIndex, aDiscIndex+1, pNumDisclosures, aTotalCollisions, aTotalMatches)
	}
	return nil
}

// Main entry point for the P6 simulation. This simulates the number of collisions that might occur given a configuration supplied as
// an argument.
func main() {
	// show the usage if we don't have a single argument
	if len(os.Args) != 2 {
		fmt.Println("P6 Simulation Tool")
		fmt.Println("Copyright (c) 2020 CriticalBlue Ltd.")
		fmt.Println()
		fmt.Println("Parameter must be a filename of a JSON file with the configuration, containing map of:")
		fmt.Println("  numberOfDisclosures: number of simulated epoch disclosures")
		fmt.Println("  numberOfReceivers: number of simulated receivers")
		fmt.Println("  receptionProfile: list of floats for proximate transmitter probabilities (first entry for none)")
		fmt.Println("  receptionMaxBurstPeriod: maximum contiguous frames associated with reception or not (chosen randomly up to this value)")
		fmt.Println("  epochLength: length of an epoch in terms of the number of frames")
		fmt.Println("  matchesNeeded: number of matches needed in a cluster for a collision")
		fmt.Println("  clusterSlices: number of slices in a cluster in which sufficient matches are required")
		os.Exit(1)
	}

	// read the file content
	aBytes, aErr := ioutil.ReadFile(os.Args[1])
	if aErr != nil {
		fmt.Println("Cannot read configuration file: " + os.Args[1])
		os.Exit(1)
	}

	// unmarshal the supplied configuration
	var aConfig Config
	aErr = json.Unmarshal(aBytes, &aConfig)
	if aErr != nil {
		fmt.Println("Cannot unmarshal configuration: " + aErr.Error())
		os.Exit(1)
	}

	// build simulated traces for all the receivers
	aReceiversPerRoutine := (aConfig.NumberOfReceivers / sNumGoRoutines) + 1
	aTotalReceivers := aReceiversPerRoutine * sNumGoRoutines
	fmt.Printf("Creating %d simulated receivers\n", aTotalReceivers)
	aReceivers := make([]*ReceptionTrace, aTotalReceivers, aTotalReceivers)
	var aRecWaitGroup sync.WaitGroup
	aRecWaitGroup.Add(sNumGoRoutines)
	var ReceiversMutex sync.Mutex
	for aGoIndex := 0; aGoIndex < sNumGoRoutines; aGoIndex++ {
		go func(pGoIndex int) {
			for aRecIndex := 0; aRecIndex < aReceiversPerRoutine; aRecIndex++ {
				aReceptionTrace, aErr := genSimulatedReceiver(aConfig.EpochLength, aConfig.ReceptionProfile, aConfig.ReceptionMaxBurstPeriod)
				if aErr != nil {
					fmt.Println("Error building receiver traces: " + aErr.Error())
					os.Exit(1)
				}
				if (aRecIndex % 1000) == 0 {
					fmt.Printf("creating receiver routine %d, %d/%d\n", pGoIndex, aRecIndex+1, aReceiversPerRoutine)
				}
				aIndex := (pGoIndex * aReceiversPerRoutine) + aRecIndex
				ReceiversMutex.Lock()
				aReceivers[aIndex] = aReceptionTrace
				ReceiversMutex.Unlock()
			}
			aRecWaitGroup.Done()
		}(aGoIndex)
	}
	aRecWaitGroup.Wait()

	// simulate a number of disclosures occuring in different go routines
	fmt.Println("Starting disclosure simulation")
	var aWaitGroup sync.WaitGroup
	aWaitGroup.Add(sNumGoRoutines)
	aDisclosuresPerRoutine := (aConfig.NumberOfDisclosures / sNumGoRoutines) + 1
	for aGoIndex := 0; aGoIndex < sNumGoRoutines; aGoIndex++ {
		go func(pGoIndex int) {
			aErr := simulateDisclosures(aReceivers, pGoIndex, aDisclosuresPerRoutine, aConfig.EpochLength, aConfig.MatchesNeeded, aConfig.ClusterSlices)
			if aErr != nil {
				fmt.Println("Error simulating disclosures: " + aErr.Error())
				os.Exit(1)
			}
			aWaitGroup.Done()
		}(aGoIndex)
	}
	aWaitGroup.Wait()

	// we completed execution successfully
	fmt.Println("Total collisions = " + strconv.Itoa(sTotalCollisions))
	os.Exit(0)
}
