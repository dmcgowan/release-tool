/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package main

import (
	"sort"
	"testing"
)

func TestParseModuleCommit(t *testing.T) {
	for i, tc := range []struct {
		str    string
		commit string
		isSha  bool
	}{
		{"v16.2.1+incompatible", "v16.2.1", false},
		{"v0.0.0-20171204204709-577dee27f20d", "577dee27f20d", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0-rc1", "v1.0.0-rc1", false},
		{"v0.4.15-0.20190919025122-fc70bd9a86b5", "fc70bd9a86b5", true},
	} {
		commit, isSha := getCommitOrVersion(tc.str)
		if commit != tc.commit {
			t.Fatalf("[%d] unexpected commit %q, expected %q", i, commit, tc.commit)
		}
		if isSha != tc.isSha {
			t.Fatalf("[%d] unexpected sha %t, expected %t", i, isSha, tc.isSha)
		}

	}
}

func TestGetGitURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		git  string
	}{
		{"github.com/docker/distribution", "https://github.com/docker/distribution"},
		{"sigs.k8s.io/yaml", "https://github.com/kubernetes-sigs/yaml"},
		{"sigs.k8s.io/yaml/v2", "https://github.com/kubernetes-sigs/yaml"},
		{"k8s.io/utils", "https://github.com/kubernetes/utils"},
		{"k8s.io/utils/v8", "https://github.com/kubernetes/utils"},
		{"k8s.io/client-go", "https://github.com/kubernetes/client-go"},
		{"github.com/someorg/somerepo/v2", "https://github.com/someorg/somerepo"},
		{"github.com/someorg/somerepo/unnecessarysubmod", "https://github.com/someorg/somerepo"},
		{"github.com/invalid", ""},
		//{"gopkg.in/src-d/go-git.v4", "https://github.com/src-d/go-git"},
		//{"golang.org/x/tools", "https://github.com/golang/tools"},
		//{"golang.org/x/sync", "https://github.com/golang/sync"},
	} {
		git := getGitURL(tc.name)
		if git != tc.git {
			t.Errorf("[%s] unexpected git url %q, expected %q", tc.name, git, tc.git)
		}

	}

}

// mapCache is an in-memory Cache used in tests to pre-populate SHA values
// without invoking git or making network calls.
type mapCache map[string][]byte

func (m mapCache) Get(key string) ([]byte, bool) {
	v, ok := m[key]
	return v, ok
}

func (m mapCache) Put(key string, value []byte) error {
	m[key] = value
	return nil
}

// shaKey builds the cache key that getSha uses for a given gitURL + rev,
// matching the format in getSha: "git ls-remote <url> <rev> <rev>^{}"
func shaKey(gitURL, rev string) string {
	return "git ls-remote " + gitURL + " " + rev + " " + rev + "^{}"
}

func TestGetUpdatedDeps(t *testing.T) {
	const (
		shaA = "aaaaaaaaaaaa"
		shaB = "bbbbbbbbbbbb"
	)

	// dep builds a dependency with a GitHub URL so getGitURL can resolve it
	// without a network call.
	dep := func(name, ref, sha string) dependency {
		return dependency{
			Name:   name,
			Ref:    ref,
			Sha:    sha,
			GitURL: "https://github.com/example/" + name,
		}
	}

	for _, tc := range []struct {
		name     string
		previous []dependency
		current  []dependency
		cache    mapCache
		// want lists the dep names expected in the output, in any order.
		want []string
	}{
		{
			// Regression: semver bump where SHA lookup silently returns ""; dep
			// must still appear in the output.
			name: "semver bump - sha lookup yields empty (lookup failed)",
			previous: []dependency{dep("crypto", "v0.49.0", "")},
			current:  []dependency{dep("crypto", "v0.50.0", "")},
			// Empty cache → getSha will call git ls-remote which will fail
			// (no git remote in test environment) → errLookupFailed → should
			// fall back to Ref comparison and INCLUDE the dep.
			cache: mapCache{},
			want:  []string{"crypto"},
		},
		{
			// Regression: semver bump where SHA lookup succeeds with different
			// SHAs (the normal happy path) — dep must appear.
			name:     "semver bump - different shas",
			previous: []dependency{dep("mylib", "v1.0.0", "")},
			current:  []dependency{dep("mylib", "v1.1.0", "")},
			cache: mapCache{
				shaKey("https://github.com/example/mylib", "v1.0.0"): []byte(shaA),
				shaKey("https://github.com/example/mylib", "v1.1.0"): []byte(shaB),
			},
			want: []string{"mylib"},
		},
		{
			// A semver tag and its pseudo-version resolve to the SAME commit —
			// dep should be suppressed (they're aliases).
			name:     "semver alias - same sha, suppress",
			previous: []dependency{dep("mylib", "v0.0.0-20210101000000-aaaaaaaaaaaa", shaA)},
			current:  []dependency{dep("mylib", "v1.0.0", "")},
			cache: mapCache{
				shaKey("https://github.com/example/mylib", "v1.0.0"): []byte(shaA),
			},
			want: []string{},
		},
		{
			// Two pseudo-version SHAs that differ — dep must appear.
			name: "pseudo-version sha change",
			previous: []dependency{dep("policy", "v0.0.0-20260324161837-b7c0b994300b", "b7c0b994300b")},
			current:  []dependency{dep("policy", "v0.0.0-20260507153417-a39d60132186", "a39d60132186")},
			cache:    mapCache{},
			want:     []string{"policy"},
		},
		{
			// Only the previous ref resolves to a SHA; current has no tag.
			// Cannot confirm they're the same commit → include.
			name:     "partial resolution - only previous sha resolved",
			previous: []dependency{dep("mylib", "v1.0.0", "")},
			current:  []dependency{dep("mylib", "v1.1.0", "")},
			cache: mapCache{
				shaKey("https://github.com/example/mylib", "v1.0.0"): []byte(shaA),
				// v1.1.0 intentionally absent → getSha returns ("", nil)
			},
			want: []string{"mylib"},
		},
		{
			// Only the current ref resolves to a SHA; previous has no tag.
			// Cannot confirm they're the same commit → include.
			name:     "partial resolution - only current sha resolved",
			previous: []dependency{dep("mylib", "v1.0.0", "")},
			current:  []dependency{dep("mylib", "v1.1.0", "")},
			cache: mapCache{
				// v1.0.0 intentionally absent → getSha returns ("", nil)
				shaKey("https://github.com/example/mylib", "v1.1.0"): []byte(shaB),
			},
			want: []string{"mylib"},
		},
		{
			// New dependency (not in previous) — must appear as New.
			name:     "new dependency",
			previous: []dependency{},
			current:  []dependency{dep("newlib", "v1.0.0", "")},
			cache:    mapCache{},
			want:     []string{"newlib"},
		},
		{
			// Dependency unchanged — must NOT appear.
			name:     "unchanged dependency",
			previous: []dependency{dep("stable", "v2.0.0", "")},
			current:  []dependency{dep("stable", "v2.0.0", "")},
			cache:    mapCache{},
			want:     []string{},
		},
		{
			// IgnoreDeps suppresses listed deps even when they changed.
			name:     "ignore_deps suppresses changes",
			previous: []dependency{dep("noisy", "v1.0.0", ""), dep("signal", "v1.0.0", "")},
			current:  []dependency{dep("noisy", "v1.1.0", ""), dep("signal", "v1.1.0", "")},
			cache:    mapCache{},
			want:     []string{"signal"},
		},
		{
			// OverrideDeps (Previous set on current dep) forces the comparison
			// to use the overridden Previous value instead of the previous
			// dep list.
			name: "override_deps previous",
			previous: []dependency{dep("overridden", "v1.0.0", "")},
			current: []dependency{{
				Name:     "overridden",
				Ref:      "v2.0.0",
				Previous: "v1.5.0", // override: treat v1.5.0 as the baseline
				GitURL:   "https://github.com/example/overridden",
			}},
			cache: mapCache{},
			want:  []string{"overridden"},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ignored := []string{}
			if tc.name == "ignore_deps suppresses changes" {
				ignored = []string{"noisy"}
			}
			got, err := getUpdatedDeps(tc.previous, tc.current, ignored, tc.cache)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotNames := make([]string, 0, len(got))
			for _, d := range got {
				gotNames = append(gotNames, d.Name)
			}
			sort.Strings(gotNames)
			wantSorted := append([]string{}, tc.want...)
			sort.Strings(wantSorted)
			if len(gotNames) != len(wantSorted) {
				t.Fatalf("got deps %v, want %v", gotNames, wantSorted)
			}
			for i := range gotNames {
				if gotNames[i] != wantSorted[i] {
					t.Errorf("dep[%d]: got %q, want %q", i, gotNames[i], wantSorted[i])
				}
			}
		})
	}
}

func TestReleaseNote(t *testing.T) {
	for i, tc := range []struct {
		body string
		note string
	}{
		{"", ""},
		{"not a release note", ""},
		{"```release-note\nJust a release note\n```", "Just a release note"},
		{"``` release-note\nJust a release note\n```", "Just a release note"},
		{"``` release-note\nJust a release note\n```\n", "Just a release note"},
		{"``` release-note\r\nJust a release note\r\n```", "Just a release note"},
		{"``` release-note\r\nJust a release note\r\n```\r\n", "Just a release note"},
		{"``` release-note\r\nJust a\r\nrelease note\r\n```", "Just a\nrelease note"},
		{"Pull request body\n\n```release-note\nMore than a\n**`release note`**\n```", "More than a\n**`release note`**"},
		{"Pull request body\n```release-note\nThe release note\n```\nNot Actually the end", "The release note"},
		{"Poorly formatted```release-note\nThe release note\n```\n", ""},
		{"Two blocks\n```release-note\nThe release note\n```\nAnd\n\n```something-else\ncode and more code\n```", "The release note"},
		{"Two release notes\n```release-note\nThe first release note\n```\nAnd\n\n```release-note\nThe second release note\n```", "The first release note"},
	} {
		note := getReleaseNote(tc.body)
		if note != tc.note {
			t.Errorf("[%d] unexpected release note\n\t%q\nexpected\n\t%q", i, note, tc.note)
		}
	}

}
