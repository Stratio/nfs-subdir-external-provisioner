/*
Copyright 2026 The Kubernetes Authors.

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
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/controller"
)

const intelligencePattern = "/${.PVC.annotations.instance}/analytic/users/${.PVC.annotations.path}"

func TestResolveCustomPath(t *testing.T) {
	tests := []struct {
		name        string
		pattern     string
		labels      map[string]string
		annotations map[string]string
		want        string
		wantErr     bool
	}{
		{
			name:        "annotations",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1", "path": "jdoe"},
			want:        "intelligence1/analytic/users/jdoe",
		},
		{
			name:    "labels and PVC metadata",
			pattern: "${.PVC.namespace}/${.PVC.labels.app}/${.PVC.name}",
			labels:  map[string]string{"app": "web"},
			want:    "ns/web/claim",
		},
		{
			name:        "dot-dot annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1", "path": ".."},
			wantErr:     true,
		},
		{
			name:        "traversal inside annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "..", "path": "jdoe"},
			wantErr:     true,
		},
		{
			name:        "annotation with slashes",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1", "path": "../../../intelligence2/analytic/users/jdoe"},
			wantErr:     true,
		},
		{
			name:        "absolute path annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1", "path": "/etc"},
			wantErr:     true,
		},
		{
			name:        "backslash annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1", "path": `..\..`},
			wantErr:     true,
		},
		{
			name:        "dot annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1", "path": "."},
			wantErr:     true,
		},
		{
			name:        "empty annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "", "path": "jdoe"},
			wantErr:     true,
		},
		{
			name:        "missing annotation",
			pattern:     intelligencePattern,
			annotations: map[string]string{"instance": "intelligence1"},
			wantErr:     true,
		},
		{
			name:    "unknown PVC field",
			pattern: "${.PVC.uid}",
			wantErr: true,
		},
		{
			name:    "dot-dot in pattern",
			pattern: "../${.PVC.name}",
			wantErr: true,
		},
		{
			name:    "pattern resolves to base path",
			pattern: "/",
			wantErr: true,
		},
		{
			name:    "absolute pattern stays under base path",
			pattern: "/data/${.PVC.name}",
			want:    "data/claim",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := &pvcMetadata{
				data:        map[string]string{"name": "claim", "namespace": "ns"},
				labels:      tt.labels,
				annotations: tt.annotations,
			}
			got, err := resolveCustomPath(tt.pattern, meta)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func newProvisionOptions(pattern string, annotations map[string]string) controller.ProvisionOptions {
	reclaim := v1.PersistentVolumeReclaimDelete
	params := map[string]string{}
	if pattern != "" {
		params["pathPattern"] = pattern
	}
	return controller.ProvisionOptions{
		StorageClass: &storage.StorageClass{
			ReclaimPolicy: &reclaim,
			Parameters:    params,
		},
		PVName: "pvc-1234",
		PVC: &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "claim",
				Namespace:   "ns",
				Annotations: annotations,
			},
		},
	}
}

func setupMountPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := mountPath
	mountPath = dir
	t.Cleanup(func() { mountPath = old })
	return dir
}

func TestProvision(t *testing.T) {
	p := &nfsProvisioner{server: "nfs.example.com", path: "/exports"}

	t.Run("default path", func(t *testing.T) {
		dir := setupMountPath(t)
		pv, _, err := p.Provision(context.Background(), newProvisionOptions("", nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, want := pv.Spec.NFS.Path, "/exports/ns-claim-pvc-1234"; got != want {
			t.Fatalf("got NFS path %q, want %q", got, want)
		}
		info, err := os.Stat(filepath.Join(dir, "ns-claim-pvc-1234"))
		if err != nil {
			t.Fatalf("directory not created: %v", err)
		}
		if info.Mode().Perm() != 0o777 {
			t.Fatalf("got mode %v, want 0777", info.Mode().Perm())
		}
	})

	t.Run("pathPattern with annotations", func(t *testing.T) {
		dir := setupMountPath(t)
		opts := newProvisionOptions(intelligencePattern, map[string]string{"instance": "intelligence1", "path": "jdoe"})
		pv, _, err := p.Provision(context.Background(), opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, want := pv.Spec.NFS.Path, "/exports/intelligence1/analytic/users/jdoe"; got != want {
			t.Fatalf("got NFS path %q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "intelligence1/analytic/users/jdoe")); err != nil {
			t.Fatalf("directory not created: %v", err)
		}
	})

	t.Run("traversal is rejected", func(t *testing.T) {
		dir := setupMountPath(t)
		opts := newProvisionOptions(intelligencePattern, map[string]string{"instance": "intelligence1", "path": "../../../intelligence2"})
		state, err := provisionError(p, opts)
		if err == nil {
			t.Fatal("expected error")
		}
		if state != controller.ProvisioningFinished {
			t.Fatalf("got state %q, want %q", state, controller.ProvisioningFinished)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("expected no directories to be created, got %d", len(entries))
		}
	})

	t.Run("symlink is rejected", func(t *testing.T) {
		dir := setupMountPath(t)
		victim := filepath.Join(dir, "intelligence2/analytic/users/victim")
		if err := os.MkdirAll(victim, 0o700); err != nil {
			t.Fatal(err)
		}
		users := filepath.Join(dir, "intelligence1/analytic/users")
		if err := os.MkdirAll(users, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../../intelligence2/analytic/users/victim", filepath.Join(users, "jdoe")); err != nil {
			t.Fatal(err)
		}
		opts := newProvisionOptions(intelligencePattern, map[string]string{"instance": "intelligence1", "path": "jdoe"})
		if _, err := provisionError(p, opts); err == nil {
			t.Fatal("expected error")
		}
		info, err := os.Stat(victim)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("permissions of the symlink target changed to %v", info.Mode().Perm())
		}
	})
}

func provisionError(p *nfsProvisioner, opts controller.ProvisionOptions) (controller.ProvisioningState, error) {
	_, state, err := p.Provision(context.Background(), opts)
	return state, err
}

func TestDeleteOutsideBasePath(t *testing.T) {
	setupMountPath(t)
	outside := t.TempDir()
	p := &nfsProvisioner{server: "nfs.example.com", path: "/exports"}
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv"},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				NFS: &v1.NFSVolumeSource{Server: "nfs.example.com", Path: outside},
			},
		},
	}
	if err := p.Delete(context.Background(), pv); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("directory outside of the base path was touched: %v", err)
	}
}
