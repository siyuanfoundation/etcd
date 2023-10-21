// Copyright 2017 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type Randomizer struct {
	Rand *rand.Rand
}

// Struct randomizes struct fields in the struct the pointer p points to,
// if the field is annotated with the "randomize" tag.
// Options for the "randomize" tag:
//   - "skip": skips randomize for the field
//   - "randomNumber:min,max"- generates a random number between [min, max].
//     Example: `randomize:"randomNumber:10,20"`
//   - "randomElement:elem1,elem2,elem3,etc": picks a random element from the given list.
//     Example: `randomize:"randomElement:10,20,8,678"`
//   - "randomBool:": generates a random bool value (50/50).
//     Example: `randomize:"randomBool:"`
//     Note: if the bool field name starts with "Experimental", it is randomized by default.
func (r *Randomizer) Struct(p interface{}) error {
	if r.Rand == nil {
		r.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	t := reflect.TypeOf(p)

	if t.Kind() != reflect.Ptr {
		return fmt.Errorf("input to Randomizer.Struct() must be a pointer")
	}
	t = t.Elem()
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("input to Randomizer.Struct() must be a pointer to struct")
	}
	v := reflect.ValueOf(p).Elem()
	n := t.NumField()
	for i := 0; i < n; i++ {
		elementT := t.Field(i)
		elementV := v.Field(i)
		tag, _ := elementT.Tag.Lookup("randomize")
		// Check whether or not to skip this field
		if !isRandomizable(elementT, tag) {
			// Do nothing, skip it
			continue
		}
		// bool randomizer is enabled for experimental flags by default.
		if elementT.Type.Name() == "bool" && len(tag) == 0 {
			tag = "randomBool:"
		}
		fs := strings.Split(tag, ":")
		if len(fs) != 2 {
			fmt.Errorf("wanted func:argments for the randomize tag, but got %s\n", tag)
		}
		f, ok := randFuncLookup[fs[0]]
		if !ok {
			return fmt.Errorf("cannot find randomize function %s\n", fs[0])
		}
		valStr, err := f(r.Rand, fs[1])
		if err != nil {
			return err
		}
		switch elementT.Type.Name() {
		case "bool":
			val, _ := strconv.ParseBool(valStr)
			elementV.SetBool(val)
		case "int", "int32", "int64":
			val, _ := strconv.ParseInt(valStr, 10, 64)
			elementV.SetInt(val)
		case "uint", "uint32", "uint64":
			val, _ := strconv.ParseInt(valStr, 10, 64)
			elementV.SetUint(uint64(val))
		}
	}
	return nil
}

func isRandomizable(field reflect.StructField, tag string) bool {
	if !field.IsExported() {
		return false
	}
	if tag == "skip" {
		return false
	}
	switch field.Type.Name() {
	case "bool":
		// by default, randomize is turned on for experimental bool flags,
		// and off for non-experimental flag if no randomize tag is specified.
		return strings.HasPrefix(field.Name, "Experimental") || len(tag) > 0
	case "int", "int32", "int64":
	case "uint", "uint32", "uint64":
	// case "time.Duration":
	default:
		return false
	}
	// all non-bool types need to specify the random function in the tag to be radomizable.
	return len(tag) > 0
}

type randFunc = func(*rand.Rand, string) (string, error)

var randFuncLookup = map[string]randFunc{
	"randomBool":    randomBool,
	"randomNumber":  randomNumber,
	"randomElement": randomElement,
}

func randomBool(r *rand.Rand, _ string) (string, error) {
	return strconv.FormatBool(r.Intn(2) == 1), nil
}

// randomNumber expects the tag string to be "randomNumber:min,max"
// and generates a random number between [min, max].
func randomNumber(r *rand.Rand, argString string) (number string, err error) {
	args := strings.Split(argString, ",")
	if len(args) != 2 {
		return "", fmt.Errorf("expeted 2 number arguments for randomNumber, but got (%s)", argString)
	}
	var min, max int
	if min, err = strconv.Atoi(args[0]); err != nil {
		return
	}
	if max, err = strconv.Atoi(args[1]); err != nil {
		return
	}
	if min > max {
		return "", fmt.Errorf("randomNumber: min %d > max %d", min, max)
	}
	return strconv.Itoa(r.Intn(max+1-min) + min), nil
}

// randomNumber expects the tag string to be "randomElement:elem1,elem2,elem3,etc"
// and picks a random element from the given list.
func randomElement(r *rand.Rand, argString string) (string, error) {
	args := strings.Split(argString, ",")
	if len(args) < 1 {
		return "", fmt.Errorf("empty list for randomElement")
	}
	i := r.Intn(len(args))
	return args[i], nil
}
