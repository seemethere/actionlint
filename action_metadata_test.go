package actionlint

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
)

func testGetWantedActionMetadata() *ActionMetadata {
	want := &ActionMetadata{
		Name: "My action",
		Inputs: map[string]ActionMetadataInputRequired{
			"name":     false,
			"message":  true,
			"addition": false,
		},
		Outputs: map[string]struct{}{
			"user_id": {},
		},
	}
	return want
}

// Normal cases

func TestLocalActionsFindMetadata(t *testing.T) {
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}
	c := NewLocalActionsCache(proj, "", nil)

	want := testGetWantedActionMetadata()
	for _, spec := range []string{"./action-yml", "./action-yaml"} {
		t.Run(spec, func(t *testing.T) {
			// read metadata repeatedly (should be cached)
			for i := 0; i < 3; i++ {
				have, err := c.FindMetadata(spec)
				if err != nil {
					t.Fatal(i, err)
				}
				if have == nil {
					t.Fatal(i, "metadata is nil")
				}
				if !cmp.Equal(want, have) {
					t.Fatal(i, cmp.Diff(want, have))
				}
			}

			cached, ok := c.cache[spec]
			if !ok {
				t.Fatal("metadata was not cached", c.cache)
			}
			if cached == nil {
				t.Fatal("cached metadata is nil", c.cache)
			}
		})
	}

	t.Run("./empty", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			m, err := c.FindMetadata("./empty")
			if err != nil {
				t.Fatal(i, err)
			}
			if m == nil {
				t.Fatal(i, "metadata is nil")
			}
			if len(m.Inputs) != 0 {
				t.Fatal("inputs are not empty", m.Inputs)
			}
			if len(m.Outputs) != 0 {
				t.Fatal("outputs are not empty", m.Outputs)
			}
		}
	})
}

func TestLocalActionsFindConcurrently(t *testing.T) {
	n := 10
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}
	c := NewLocalActionsCache(proj, "", nil)
	ret := make(chan *ActionMetadata)
	err := make(chan error)

	for i := 0; i < n; i++ {
		go func() {
			m, e := c.FindMetadata("./action-yml")
			if e != nil {
				err <- e
				return
			}
			ret <- m
		}()
	}

	ms := []*ActionMetadata{}
	errs := []error{}
	for i := 0; i < n; i++ {
		select {
		case m := <-ret:
			ms = append(ms, m)
		case e := <-err:
			errs = append(errs, e)
		}
	}

	if len(errs) != 0 {
		t.Fatal("error occurred:", errs)
	}

	want := testGetWantedActionMetadata()
	for _, have := range ms {
		if !cmp.Equal(want, have) {
			t.Fatal(cmp.Diff(want, have))
		}
	}

	cached, ok := c.cache["./action-yml"]
	if !ok {
		t.Fatal("metadata was not cached", c.cache)
	}
	if cached == nil {
		t.Fatal("cached metadata is nil", c.cache)
	}
}

func TestLocalActionsProjectIsNil(t *testing.T) {
	c := NewLocalActionsCache(nil, "", nil)
	for _, spec := range []string{"./action-yml", "this-action-does-not-exit"} {
		m, err := c.FindMetadata(spec)
		if err != nil {
			t.Fatal(spec, "error occurred:", err)
		}
		if m != nil {
			t.Fatal(spec, "metadata was parsed", m)
		}
	}
}

func TestLocalActionsIgnoreRemoteActions(t *testing.T) {
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}
	c := NewLocalActionsCache(proj, "", nil)
	for _, spec := range []string{"actions/checkout@v2", "docker://example.com/foo/bar"} {
		m, err := c.FindMetadata(spec)
		if err != nil {
			t.Fatal(spec, "error occurred:", err)
		}
		if m != nil {
			t.Fatal(spec, "metadata was parsed", m)
		}
	}
}

func TestLocalActionsLogCacheHit(t *testing.T) {
	dbg := &bytes.Buffer{}
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}
	c := NewLocalActionsCache(proj, "", dbg)

	want := testGetWantedActionMetadata()
	for i := 0; i < 2; i++ {
		have, err := c.FindMetadata("./action-yml")
		if err != nil {
			t.Fatal(err)
		}
		if !cmp.Equal(want, have) {
			t.Fatal(cmp.Diff(want, have))
		}
	}

	logs := strings.Split(strings.TrimSpace(dbg.String()), "\n")
	if len(logs) != 2 {
		t.Fatalf("2 logs were expected but got %d logs: %#v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "New metadata parsed from action "+filepath.Join("testdata", "action_metadata", "action-yml")) {
		t.Fatalf("first log should be 'new metadata' but got %q", logs[0])
	}
	if !strings.Contains(logs[1], "Cache hit for ./action-yml") {
		t.Fatalf("second log should be 'cache hit' but got %q", logs[1])
	}
}

// Error cases

func TestLocalActionsFailures(t *testing.T) {
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}

	testCases := []struct {
		what string
		spec string
		want string
	}{
		{
			what: "file not found",
			spec: "./this-action-does-not-exist",
			want: "neither action.yaml nor action.yml is found in directory",
		},
		{
			what: "broken metadata YAML",
			spec: "./broken",
			want: "invalid: yaml: line 2:",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.what, func(t *testing.T) {
			c := NewLocalActionsCache(proj, "", nil)
			m, err := c.FindMetadata(tc.spec)
			if err == nil {
				t.Fatal("error was not returned", m)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatal("unexpected error:", err)
			}

			// Second try does not return error, but metadata is also nil not to show the same error from
			// multiple rules.
			m, err = c.FindMetadata(tc.spec)
			if err != nil {
				t.Fatal("error was returned at second try", err)
			}
			if m != nil {
				t.Fatal("metadata was not nil even if it does not exist", m)
			}

			m, ok := c.cache[tc.spec]
			if !ok {
				t.Fatal("error was not cached", c.cache)
			}
			if m != nil {
				t.Fatal("metadata should be nil when it caused an error", c.cache)
			}
		})
	}
}

func TestLocalActionsConcurrentFailures(t *testing.T) {
	n := 10
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}
	c := NewLocalActionsCache(proj, "", nil)
	errC := make(chan error)

	for i := 0; i < n; i++ {
		go func() {
			_, err := c.FindMetadata("./this-action-does-not-exist")
			errC <- err
		}()
	}

	errs := []error{}
	for i := 0; i < n; i++ {
		errs = append(errs, <-errC)
	}

	// At least once error was reported
	var err error
	for _, e := range errs {
		if e != nil {
			err = e
			break
		}
	}

	if err == nil {
		t.Fatal("error did not occur", err)
	}
	if !strings.Contains(err.Error(), "neither action.yaml nor action.yml is found in directory") {
		t.Fatal("unexpected error:", err)
	}
}

func TestLocalActionsConcurrentMultipleMetadataAndFailures(t *testing.T) {
	proj := &Project{filepath.Join("testdata", "action_metadata"), nil}
	c := NewLocalActionsCache(proj, "", nil)

	inputs := []string{
		"./action-yml",
		"./action-yaml",
		"./action-yml",
		"./this-action-does-not-exist",
		"./action-yaml",
		"./action-yaml",
		"./this-action-does-not-exist",
		"./action-yml",
		"./this-action-does-not-exist",
		"./action-yaml",
	}

	reqC := make(chan string)
	retC := make(chan *ActionMetadata)
	errC := make(chan error)
	done := make(chan struct{})

	for i := 0; i < 3; i++ {
		go func() {
			for {
				select {
				case spec := <-reqC:
					m, err := c.FindMetadata(spec)
					if m == nil {
						errC <- err
						break
					}
					retC <- m
				case <-done:
					return
				}
			}
		}()
	}

	go func() {
		for _, in := range inputs {
			select {
			case reqC <- in:
			case <-done:
				return
			}
		}
	}()

	ret := []*ActionMetadata{}
	errs := []error{}
	for i := 0; i < len(inputs); i++ {
		select {
		case m := <-retC:
			ret = append(ret, m)
		case err := <-errC:
			errs = append(errs, err)
		}
	}
	close(done)

	numErrs := 0
	for _, in := range inputs {
		if in == "./this-action-does-not-exist" {
			numErrs++
		}
	}
	numRet := len(inputs) - numErrs

	if len(errs) != numErrs {
		t.Fatalf("wanted %d errors but got %d: %v", numErrs, len(errs), errs)
	}
	if len(ret) != numRet {
		t.Fatalf("wanted %d errors but got %d: %v", numRet, len(ret), ret)
	}

	var err error
	for _, e := range errs {
		if e != nil {
			err = e
			break
		}
	}

	if err == nil {
		t.Fatal("error did not occur", err)
	}
	if !strings.Contains(err.Error(), "neither action.yaml nor action.yml is found in directory") {
		t.Fatal("unexpected error:", err)
	}

	want := testGetWantedActionMetadata()
	for _, have := range ret {
		if !cmp.Equal(want, have) {
			t.Fatal("unexpected metadata:", cmp.Diff(want, have))
		}
	}
}

func TestActionMetadataYAMLUnmarshalOK(t *testing.T) {
	testCases := []struct {
		what  string
		input string
		want  ActionMetadata
	}{
		{
			what:  "no input and no output",
			input: `name: Test`,
			want: ActionMetadata{
				Name: "Test",
			},
		},
		{
			what: "inputs",
			input: `name: Test
inputs:
  input1:
    description: test
  input2:
    description: test
    required: false
  input3:
    description: test
    required: true
    default: 'default'
  input4:
    description: test
    required: false
    default: 'default'
  input5:
    description: test
    required: true`,
			want: ActionMetadata{
				Name: "Test",
				Inputs: map[string]ActionMetadataInputRequired{
					"input1": false,
					"input2": false,
					"input3": false,
					"input4": false,
					"input5": true,
				},
			},
		},
		{
			what: "outputs",
			input: `name: Test
outputs:
  output1:
    description: test
  output2:
    description: test
`,
			want: ActionMetadata{
				Name: "Test",
				Outputs: map[string]struct{}{
					"output1": {},
					"output2": {},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.what, func(t *testing.T) {
			var have ActionMetadata
			if err := yaml.Unmarshal([]byte(tc.input), &have); err != nil {
				t.Fatal(err)
			}
			if !cmp.Equal(&tc.want, &have) {
				t.Fatal(cmp.Diff(&tc.want, &have))
			}
		})
	}
}

func TestActionMetadataYAMLUnmarshalError(t *testing.T) {
	testCases := []struct {
		what  string
		input string
		want  string
	}{
		{
			what: "invalid inputs",
			input: `name: Test
inputs: "foo"`,
			want: "into map[string]actionlint.ActionMetadataInputRequired",
		},
		{
			what: "invalid inputs.*",
			input: `name: Test
inputs:
  input1: "foo"`,
			want: "into actionlint.actionInputMetadata",
		},
		{
			what: "invalid outputs",
			input: `name: Test
outputs: "foo"`,
			want: "into map[string]struct {}",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.what, func(t *testing.T) {
			var data ActionMetadata
			err := yaml.Unmarshal([]byte(tc.input), &data)
			if err == nil {
				t.Fatal("error did not occur")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("%q is not contained in error message %q", tc.want, msg)
			}
		})
	}
}

func TestLocalActionsCacheFactory(t *testing.T) {
	f := NewLocalActionsCacheFactory("/path/to/dir", io.Discard)
	p1 := &Project{"path/to/project1", nil}
	c1 := f.GetCache(p1)

	if c1.cwd != f.cwd {
		t.Errorf("cwd %q is not propagated: %q", f.cwd, c1.cwd)
	}

	p2 := &Project{"path/to/project2", nil}
	c2 := f.GetCache(p2)
	if c1 == c2 {
		t.Errorf("different cache was not created: %v", c1)
	}

	c3 := f.GetCache(p1)
	if c1 != c3 {
		t.Errorf("same cache is not returned for the same project: %v vs %v", c1, c3)
	}
}
