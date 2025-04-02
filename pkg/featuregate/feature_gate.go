// Copyright 2024 The etcd Authors
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

// Package featuregate is copied from k8s.io/component-base@v0.30.1 to avoid any potential circular dependency between k8s and etcd.
package featuregate

import (
	"flag"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coreos/go-semver/semver"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
)

type Feature string

const (
	defaultFlagName = "feature-gates"

	// allAlphaGate is a global toggle for alpha features. Per-feature key
	// values override the default set by allAlphaGate. Examples:
	//   AllAlpha=false,NewFeature=true  will result in newFeature=true
	//   AllAlpha=true,NewFeature=false  will result in newFeature=false
	allAlphaGate Feature = "AllAlpha"

	// allBetaGate is a global toggle for beta features. Per-feature key
	// values override the default set by allBetaGate. Examples:
	//   AllBeta=false,NewFeature=true  will result in NewFeature=true
	//   AllBeta=true,NewFeature=false  will result in NewFeature=false
	allBetaGate Feature = "AllBeta"
)

var (
	// The generic features.
	defaultFeatures = map[Feature]VersionedSpecs{
		allAlphaGate: {{Default: false, PreRelease: Alpha, Version: majorMinor(0, 0)}},
		allBetaGate:  {{Default: false, PreRelease: Beta, Version: majorMinor(0, 0)}},
	}

	// Special handling for a few gates.
	specialFeatures = map[Feature]func(known map[Feature]VersionedSpecs, enabled map[Feature]bool, val bool, cVer *semver.Version){
		allAlphaGate: setUnsetAlphaGates,
		allBetaGate:  setUnsetBetaGates,
	}
)

type FeatureSpec struct {
	// Default is the default enablement state for the feature
	Default bool
	// LockToDefault indicates that the feature is locked to its default and cannot be changed
	LockToDefault bool
	// PreRelease indicates the maturity level of the feature
	PreRelease prerelease
	// Version indicates the earliest version from which this FeatureSpec is valid.
	// If multiple FeatureSpecs exist for a Feature, the one with the highest version that is less
	// than or equal to the effective version of the component is used.
	Version *semver.Version
}

type VersionedSpecs []FeatureSpec

func (g VersionedSpecs) Len() int { return len(g) }
func (g VersionedSpecs) Less(i, j int) bool {
	return g[i].Version.LessThan(*g[j].Version)
}
func (g VersionedSpecs) Swap(i, j int) { g[i], g[j] = g[j], g[i] }

type aggregateError []error

func (ae aggregateError) Error() string {
	var errs []string
	for _, err := range ae {
		errs = append(errs, err.Error())
	}
	return strings.Join(errs, ", ")
}

type prerelease string

const (
	PreAlpha = prerelease("PRE-ALPHA")
	// Values for PreRelease.
	Alpha = prerelease("ALPHA")
	Beta  = prerelease("BETA")
	GA    = prerelease("")

	// Deprecated
	Deprecated = prerelease("DEPRECATED")
)

// FeatureGate indicates whether a given feature is enabled or not
type FeatureGate interface {
	// Enabled returns true if the key is enabled.
	Enabled(key Feature) bool
	// KnownFeatures returns a slice of strings describing the FeatureGate's known features.
	KnownFeatures() []string
	// DeepCopy returns a deep copy of the FeatureGate object, such that gates can be
	// set on the copy without mutating the original. This is useful for validating
	// config against potential feature gate changes before committing those changes.
	DeepCopy() MutableVersionedFeatureGate
	// String returns a string containing all enabled feature gates, formatted as "key1=value1,key2=value2,...".
	String() string
}

// MutableFeatureGate parses and stores flag gates for known features from
// a string like feature1=true,feature2=false,...
type MutableFeatureGate interface {
	FeatureGate

	// AddFlag adds a flag for setting global feature gates to the specified FlagSet.
	AddFlag(fs *flag.FlagSet, flagName string)
	// Set parses and stores flag gates for known features
	// from a string like feature1=true,feature2=false,...
	Set(value string) error
	// SetFromMap stores flag gates for known features from a map[string]bool or returns an error
	SetFromMap(m map[string]bool) error
	// Add adds features to the featureGate.
	Add(features map[Feature]FeatureSpec) error
	// GetAll returns a copy of the map of known feature names to feature specs.
	GetAll() map[Feature]FeatureSpec
	// OverrideDefault sets a local override for the registered default value of a named
	// feature. If the feature has not been previously registered (e.g. by a call to Add), has a
	// locked default, or if the gate has already registered itself with a FlagSet, a non-nil
	// error is returned.
	//
	// When two or more components consume a common feature, one component can override its
	// default at runtime in order to adopt new defaults before or after the other
	// components. For example, a new feature can be evaluated with a limited blast radius by
	// overriding its default to true for a limited number of components without simultaneously
	// changing its default for all consuming components.
	OverrideDefault(name Feature, override bool) error
}

// MutableVersionedFeatureGate parses and stores flag gates for known features from
// a string like feature1=true,feature2=false,...
// MutableVersionedFeatureGate sets options based on the emulated version of the featured gate.
type MutableVersionedFeatureGate interface {
	MutableFeatureGate
	// EmulationVersion returns the version the feature gate is set to emulate.
	// If set, the feature gate would enable/disable features based on
	// feature availability and pre-release at the emulated version instead of the binary version.
	EmulationVersion() *semver.Version
	// SetEmulationVersion overrides the emulationVersion of the feature gate.
	// Otherwise, the emulationVersion will be the same as the binary version.
	// If set, the feature defaults and availability will be as if the binary is at the emulated version.
	SetEmulationVersion(emulationVersion *semver.Version) error
	// GetAll returns a copy of the map of known feature names to versioned feature specs.
	GetAllVersioned() map[Feature]VersionedSpecs
	// AddVersioned adds versioned feature specs to the featureGate.
	AddVersioned(features map[Feature]VersionedSpecs) error
	// OverrideDefaultAtVersion sets a local override for the registered default value of a named
	// feature for the prerelease lifecycle the given version is at.
	// If the feature has not been previously registered (e.g. by a call to Add),
	// has a locked default, or if the gate has already registered itself with a FlagSet, a non-nil
	// error is returned.
	//
	// When two or more components consume a common feature, one component can override its
	// default at runtime in order to adopt new defaults before or after the other
	// components. For example, a new feature can be evaluated with a limited blast radius by
	// overriding its default to true for a limited number of components without simultaneously
	// changing its default for all consuming components.
	OverrideDefaultAtVersion(name Feature, override bool, ver *semver.Version) error
	// DeepCopyAndReset copies all the registered features of the FeatureGate object, with all the known features and overrides,
	// and resets all the enabled status of the new feature gate.
	// This is useful for creating a new instance of feature gate without inheriting all the enabled configurations of the base feature gate.
	DeepCopyAndReset() MutableVersionedFeatureGate
}

// featureGate implements FeatureGate as well as pflag.Value for flag parsing.
type featureGate struct {
	lg *zap.Logger

	featureGateName string

	special map[Feature]func(map[Feature]VersionedSpecs, map[Feature]bool, bool, *semver.Version)

	// lock guards writes to known, enabled, and reads/writes of closed
	lock sync.Mutex
	// known holds a map[Feature]FeatureSpec
	known atomic.Value
	// enabled holds a map[Feature]bool
	enabled atomic.Value
	// enabledRaw holds a raw map[string]bool of the parsed flag.
	// It keeps the original values of "special" features like "all alpha gates",
	// while enabled keeps the values of all resolved features.
	enabledRaw atomic.Value
	// closed is set to true when AddFlag is called, and prevents subsequent calls to Add
	closed           bool
	emulationVersion atomic.Pointer[semver.Version]
}

func setUnsetAlphaGates(known map[Feature]VersionedSpecs, enabled map[Feature]bool, val bool, cVer *semver.Version) {
	for k, v := range known {
		if k == "AllAlpha" || k == "AllBeta" {
			continue
		}
		featureSpec := featureSpecAtEmulationVersion(v, cVer)
		if featureSpec.PreRelease == Alpha {
			if _, found := enabled[k]; !found {
				enabled[k] = val
			}
		}
	}
}

func setUnsetBetaGates(known map[Feature]VersionedSpecs, enabled map[Feature]bool, val bool, cVer *semver.Version) {
	for k, v := range known {
		if k == "AllAlpha" || k == "AllBeta" {
			continue
		}
		featureSpec := featureSpecAtEmulationVersion(v, cVer)
		if featureSpec.PreRelease == Beta {
			if _, found := enabled[k]; !found {
				enabled[k] = val
			}
		}
	}
}

// Set, String, and Type implement pflag.Value
var _ pflag.Value = &featureGate{}

// NewVersionedFeatureGate creates a feature gate with the emulation version set to the provided version.
// SetEmulationVersion can be called after to change emulation version to a desired value.
func NewVersionedFeatureGate(name string, lg *zap.Logger, emulationVersion *semver.Version) *featureGate {
	if lg == nil {
		lg = zap.NewNop()
	}
	if emulationVersion == nil {
		emulationVersion = majorMinor(0, 0)
	}
	known := map[Feature]VersionedSpecs{}
	for k, v := range defaultFeatures {
		known[k] = v
	}

	f := &featureGate{
		lg:              lg,
		featureGateName: name,
		special:         specialFeatures,
	}
	f.known.Store(known)
	f.enabled.Store(map[Feature]bool{})
	f.enabledRaw.Store(map[string]bool{})
	f.emulationVersion.Store(emulationVersion)
	return f
}

// Set parses a string of the form "key1=value1,key2=value2,..." into a
// map[string]bool of known keys or returns an error.
func (f *featureGate) Set(value string) error {
	m := make(map[string]bool)
	for _, s := range strings.Split(value, ",") {
		if len(s) == 0 {
			continue
		}
		arr := strings.SplitN(s, "=", 2)
		k := strings.TrimSpace(arr[0])
		if len(arr) != 2 {
			return fmt.Errorf("missing bool value for %s", k)
		}
		v := strings.TrimSpace(arr[1])
		boolValue, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid value of %s=%s, err: %v", k, v, err)
		}
		m[k] = boolValue
	}
	return f.SetFromMap(m)
}

// unsafeSetFromMap stores flag gates for known features from a map[string]bool into an enabled map.
func (f *featureGate) unsafeSetFromMap(enabled map[Feature]bool, m map[string]bool, emulationVersion *semver.Version) []error {
	var errs []error
	// Copy existing state
	known := map[Feature]VersionedSpecs{}
	for k, v := range f.known.Load().(map[Feature]VersionedSpecs) {
		known[k] = v
	}

	for k, v := range m {
		key := Feature(k)
		versionedSpecs, ok := known[key]
		if !ok {
			// early return if encounters an unknown feature.
			errs = append(errs, fmt.Errorf("unrecognized feature gate: %s", k))
			return errs
		}
		featureSpec := featureSpecAtEmulationVersion(versionedSpecs, emulationVersion)
		if featureSpec.LockToDefault && featureSpec.Default != v {
			errs = append(errs, fmt.Errorf("cannot set feature gate %v to %v, feature is locked to %v", k, v, featureSpec.Default))
			continue
		}
		// Handle "special" features like "all alpha gates"
		if fn, found := f.special[key]; found {
			fn(known, enabled, v, emulationVersion)
			enabled[key] = v
			continue
		}
		if featureSpec.PreRelease == PreAlpha {
			errs = append(errs, fmt.Errorf("cannot set feature gate %v to %v, feature is PreAlpha at emulated version %s", k, v, emulationVersion.String()))
			continue
		}
		enabled[key] = v

		if featureSpec.PreRelease == Deprecated {
			f.lg.Warn(fmt.Sprintf("Setting deprecated feature gate %s=%t. It will be removed in a future release.", k, v))
		} else if featureSpec.PreRelease == GA {
			f.lg.Warn(fmt.Sprintf("Setting GA feature gate %s=%t. It will be removed in a future release.", k, v))
		}
	}
	return errs
}

// SetFromMap stores flag gates for known features from a map[string]bool or returns an error
func (f *featureGate) SetFromMap(m map[string]bool) error {
	f.lock.Lock()
	defer f.lock.Unlock()

	// Copy existing state
	enabled := map[Feature]bool{}
	for k, v := range f.enabled.Load().(map[Feature]bool) {
		enabled[k] = v
	}
	enabledRaw := map[string]bool{}
	for k, v := range f.enabledRaw.Load().(map[string]bool) {
		enabledRaw[k] = v
	}

	// Update enabledRaw first.
	// SetFromMap might be called when emulationVersion is not finalized yet, and we do not know the final state of enabled.
	// But the flags still need to be saved.
	for k, v := range m {
		enabledRaw[k] = v
	}
	f.enabledRaw.Store(enabledRaw)

	errs := f.unsafeSetFromMap(enabled, enabledRaw, f.EmulationVersion())
	if len(errs) == 0 {
		// Persist changes
		f.enabled.Store(enabled)
		f.lg.Info(fmt.Sprintf("feature gates: %v", f.enabled))
		return nil
	}
	return aggregateError(errs)
}

// String returns a string containing all enabled feature gates, formatted as "key1=value1,key2=value2,...".
func (f *featureGate) String() string {
	pairs := []string{}
	for k, v := range f.enabled.Load().(map[Feature]bool) {
		pairs = append(pairs, fmt.Sprintf("%s=%t", k, v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (f *featureGate) Type() string {
	return "mapStringBool"
}

// Add adds features to the featureGate.
func (f *featureGate) Add(features map[Feature]FeatureSpec) error {
	vs := map[Feature]VersionedSpecs{}
	for name, spec := range features {
		// if no version is provided for the FeatureSpec, it is defaulted to version 0.0 so that it can be enabled/disabled regardless of emulation version.
		spec.Version = majorMinor(0, 0)
		vs[name] = VersionedSpecs{spec}
	}
	return f.AddVersioned(vs)
}

// AddVersioned adds versioned feature specs to the featureGate.
func (f *featureGate) AddVersioned(features map[Feature]VersionedSpecs) error {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.closed {
		return fmt.Errorf("cannot add a feature gate after adding it to the flag set")
	}
	// Copy existing state
	known := f.GetAllVersioned()

	for name, specs := range features {
		if existingSpec, found := known[name]; found {
			if reflect.DeepEqual(existingSpec, specs) {
				continue
			}
			return fmt.Errorf("feature gate %q with different spec already exists: %v", name, existingSpec)
		}

		// Validate new specs are well-formed
		var lastVersion *semver.Version
		var wasBeta, wasGA, wasDeprecated bool

		for i, spec := range specs {
			if spec.Version == nil {
				return fmt.Errorf("feature %q did not provide a version", name)
			}
			if !spec.Version.Equal(*majorMinor(spec.Version.Major, spec.Version.Minor)) {
				return fmt.Errorf("feature %q specified patch version: %s", name, spec.Version.String())

			}
			// gates that begin as deprecated must indicate their prior state
			if i == 0 && spec.PreRelease == Deprecated && spec.Version.Minor != 0 {
				return fmt.Errorf("feature %q introduced as deprecated must provide a 1.0 entry indicating initial state", name)
			}
			if i > 0 {
				// versions must strictly increase
				if !lastVersion.LessThan(*spec.Version) {
					return fmt.Errorf("feature %q lists version transitions in non-increasing order (%s <= %s)", name, spec.Version, lastVersion)
				}
				// stability must not regress from ga --> {beta,alpha} or beta --> alpha, and
				// Deprecated state must be the terminal state
				switch {
				case spec.PreRelease != Deprecated && wasDeprecated:
					return fmt.Errorf("deprecated feature %q must not resurrect from its terminal state", name)
				case spec.PreRelease == Alpha && (wasBeta || wasGA):
					return fmt.Errorf("feature %q regresses stability from more stable level to %s in %s", name, spec.PreRelease, spec.Version)
				case spec.PreRelease == Beta && wasGA:
					return fmt.Errorf("feature %q regresses stability from more stable level to %s in %s", name, spec.PreRelease, spec.Version)
				}
			}
			lastVersion = spec.Version
			wasBeta = wasBeta || spec.PreRelease == Beta
			wasGA = wasGA || spec.PreRelease == GA
			wasDeprecated = wasDeprecated || spec.PreRelease == Deprecated
		}
		known[name] = specs
	}

	// Persist updated state
	f.known.Store(known)

	return nil
}

func (f *featureGate) OverrideDefault(name Feature, override bool) error {
	return f.OverrideDefaultAtVersion(name, override, f.EmulationVersion())
}

func (f *featureGate) OverrideDefaultAtVersion(name Feature, override bool, ver *semver.Version) error {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.closed {
		return fmt.Errorf("cannot override default for feature %q: gates already added to a flag set", name)
	}

	// Copy existing state
	known := f.GetAllVersioned()

	specs, ok := known[name]
	if !ok {
		return fmt.Errorf("cannot override default: feature %q is not registered", name)
	}
	spec := featureSpecAtEmulationVersion(specs, ver)
	switch {
	case spec.LockToDefault:
		return fmt.Errorf("cannot override default: feature %q default is locked to %t", name, spec.Default)
	case spec.PreRelease == PreAlpha:
		return fmt.Errorf("cannot override default: feature %q is not available before version %s", name, ver.String())
	case spec.PreRelease == Deprecated:
		f.lg.Warn(fmt.Sprintf("Overriding default of deprecated feature gate %s=%t. It will be removed in a future release.", name, override))
	case spec.PreRelease == GA:
		f.lg.Warn(fmt.Sprintf("Overriding default of GA feature gate %s=%t. It will be removed in a future release.", name, override))
	}

	spec.Default = override
	known[name] = specs
	f.known.Store(known)

	return nil
}

// GetAll returns a copy of the map of known feature names to feature specs for the current emulationVersion.
func (f *featureGate) GetAll() map[Feature]FeatureSpec {
	retval := map[Feature]FeatureSpec{}
	f.lock.Lock()
	versionedSpecs := f.GetAllVersioned()
	emuVer := f.EmulationVersion()
	f.lock.Unlock()
	for k, v := range versionedSpecs {
		spec := featureSpecAtEmulationVersion(v, emuVer)
		if spec.PreRelease == PreAlpha {
			// The feature is not available at the emulation version.
			continue
		}
		retval[k] = *spec
	}
	return retval
}

// GetAllVersioned returns a copy of the map of known feature names to versioned feature specs.
func (f *featureGate) GetAllVersioned() map[Feature]VersionedSpecs {
	retval := map[Feature]VersionedSpecs{}
	for k, v := range f.known.Load().(map[Feature]VersionedSpecs) {
		vCopy := make([]FeatureSpec, len(v))
		_ = copy(vCopy, v)
		retval[k] = vCopy
	}
	return retval
}

func featureEnabled(key Feature, enabled map[Feature]bool, known map[Feature]VersionedSpecs, emulationVersion *semver.Version) bool {
	// check explicitly set enabled list
	if v, ok := enabled[key]; ok {
		return v
	}
	if v, ok := known[key]; ok {
		return featureSpecAtEmulationVersion(v, emulationVersion).Default
	}

	panic(fmt.Errorf("feature %q is not registered in FeatureGate", key))
}

// Enabled returns true if the key is enabled.  If the key is not known, this call will panic.
func (f *featureGate) Enabled(key Feature) bool {
	// TODO: ideally we should lock the feature gate in this call to be safe, need to evaluate how much performance impact locking would have.
	v := featureEnabled(key, f.enabled.Load().(map[Feature]bool), f.known.Load().(map[Feature]VersionedSpecs), f.EmulationVersion())
	return v
}

// AddFlag adds a flag for setting global feature gates to the specified FlagSet.
func (f *featureGate) AddFlag(fs *flag.FlagSet, flagName string) {
	if flagName == "" {
		flagName = defaultFlagName
	}
	f.lock.Lock()
	// TODO(mtaufen): Shouldn't we just close it on the first Set/SetFromMap instead?
	// Not all components expose a feature gates flag using this AddFlag method, and
	// in the future, all components will completely stop exposing a feature gates flag,
	// in favor of componentconfig.
	f.closed = true
	f.lock.Unlock()

	known := f.KnownFeatures()
	fs.Var(f, flagName, ""+
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
		"Options are:\n"+strings.Join(known, "\n"))
}

// KnownFeatures returns a slice of strings describing the FeatureGate's known features.
// preAlpha, Deprecated and GA features are hidden from the list.
func (f *featureGate) KnownFeatures() []string {
	var known []string
	for k, v := range f.known.Load().(map[Feature]VersionedSpecs) {
		if k == "AllAlpha" || k == "AllBeta" {
			known = append(known, fmt.Sprintf("%s=true|false (%s - default=%t)", k, v[0].PreRelease, v[0].Default))
			continue
		}
		featureSpec := f.featureSpecAtEmulationVersion(v)
		if featureSpec.PreRelease == GA || featureSpec.PreRelease == Deprecated || featureSpec.PreRelease == PreAlpha {
			continue
		}
		known = append(known, fmt.Sprintf("%s=true|false (%s - default=%t)", k, featureSpec.PreRelease, featureSpec.Default))
	}
	sort.Strings(known)
	return known
}

// DeepCopy returns a deep copy of the FeatureGate object, such that gates can be
// set on the copy without mutating the original. This is useful for validating
// config against potential feature gate changes before committing those changes.
func (f *featureGate) DeepCopy() MutableVersionedFeatureGate {
	f.lock.Lock()
	defer f.lock.Unlock()
	// Copy existing state.
	known := f.GetAllVersioned()
	enabled := map[Feature]bool{}
	for k, v := range f.enabled.Load().(map[Feature]bool) {
		enabled[k] = v
	}
	enabledRaw := map[string]bool{}
	for k, v := range f.enabledRaw.Load().(map[string]bool) {
		enabledRaw[k] = v
	}

	// Construct a new featureGate around the copied state.
	// Note that specialFeatures is treated as immutable by convention,
	// and we maintain the value of f.closed across the copy.
	fg := &featureGate{
		special: specialFeatures,
		closed:  f.closed,
	}
	fg.emulationVersion.Store(f.EmulationVersion())
	fg.known.Store(known)
	fg.enabled.Store(enabled)
	fg.enabledRaw.Store(enabledRaw)
	return fg
}

// DeepCopyAndReset copies all the registered features of the FeatureGate object, with all the known features and overrides,
// and resets all the enabled status of the new feature gate.
// This is useful for creating a new instance of feature gate without inheriting all the enabled configurations of the base feature gate.
func (f *featureGate) DeepCopyAndReset() MutableVersionedFeatureGate {
	fg := NewVersionedFeatureGate(f.featureGateName, f.lg, f.EmulationVersion())
	known := f.GetAllVersioned()
	fg.known.Store(known)
	return fg
}

func (f *featureGate) SetEmulationVersion(emulationVersion *semver.Version) error {
	if emulationVersion.Equal(*f.EmulationVersion()) {
		return nil
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	f.lg.Info(fmt.Sprintf("set feature gate emulationVersion to %s", emulationVersion.String()))

	// Copy existing state
	enabledRaw := map[string]bool{}
	for k, v := range f.enabledRaw.Load().(map[string]bool) {
		enabledRaw[k] = v
	}
	// enabled map should be reset whenever emulationVersion is changed.
	enabled := map[Feature]bool{}
	errs := f.unsafeSetFromMap(enabled, enabledRaw, emulationVersion)

	if len(errs) == 0 {
		// Persist changes
		f.enabled.Store(enabled)
		f.emulationVersion.Store(emulationVersion)
		return nil
	}
	return aggregateError(errs)
}

func (f *featureGate) EmulationVersion() *semver.Version {
	return f.emulationVersion.Load()
}

func (f *featureGate) featureSpecAtEmulationVersion(v VersionedSpecs) *FeatureSpec {
	return featureSpecAtEmulationVersion(v, f.EmulationVersion())
}

func featureSpecAtEmulationVersion(v VersionedSpecs, emulationVersion *semver.Version) *FeatureSpec {
	i := len(v) - 1
	for ; i >= 0; i-- {
		if v[i].Version.Compare(*emulationVersion) > 0 {
			continue
		}
		return &v[i]
	}
	return &FeatureSpec{
		Default:    false,
		PreRelease: PreAlpha,
		Version:    majorMinor(0, 0),
	}
}

func majorMinor(major, minor int64) *semver.Version {
	return &semver.Version{Major: major, Minor: minor}
}
