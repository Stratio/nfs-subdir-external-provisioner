/*
Copyright 2017 The Kubernetes Authors.

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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
	v1 "k8s.io/api/core/v1"

	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	storagehelpers "k8s.io/component-helpers/storage/volume"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/controller"
)

const (
	provisionerNameKey = "PROVISIONER_NAME"
)

type nfsProvisioner struct {
	client kubernetes.Interface
	server string
	path   string
}

type pvcMetadata struct {
	data        map[string]string
	labels      map[string]string
	annotations map[string]string
}

var pattern = regexp.MustCompile(`\${\.PVC\.((labels|annotations)\.(.*?)|.*?)}`)

func (meta *pvcMetadata) stringParser(str string) (string, error) {
	result := pattern.FindAllStringSubmatch(str, -1)
	for _, r := range result {
		var value string
		switch r[2] {
		case "labels":
			value = meta.labels[r[3]]
		case "annotations":
			value = meta.annotations[r[3]]
		default:
			value = meta.data[r[1]]
		}
		if err := validatePathValue(r[0], value); err != nil {
			return "", err
		}
		str = strings.ReplaceAll(str, r[0], value)
	}

	return str, nil
}

// validatePathValue checks a value substituted into pathPattern. Labels and
// annotations are set by whoever creates the PVC, so each value must map to
// exactly one directory name: the directory layout is decided by the
// pathPattern of the StorageClass, never by the PVC.
func validatePathValue(placeholder, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("pathPattern placeholder %s resolves to an empty value", placeholder)
	case value == "." || value == "..":
		return fmt.Errorf("pathPattern placeholder %s resolves to %q, which is not allowed", placeholder, value)
	case strings.ContainsAny(value, "/\\\x00"):
		return fmt.Errorf("pathPattern placeholder %s resolves to %q, which contains a path separator or NUL character", placeholder, value)
	}
	return nil
}

// resolveCustomPath expands pathPattern and returns it as a clean path
// relative to the NFS base path. It fails if the result would point to the
// base path itself or outside of it.
func resolveCustomPath(pathPattern string, meta *pvcMetadata) (string, error) {
	customPath, err := meta.stringParser(pathPattern)
	if err != nil {
		return "", err
	}
	for _, segment := range strings.Split(customPath, "/") {
		if segment == ".." {
			return "", fmt.Errorf("path %q resolved from pathPattern must not contain \"..\"", customPath)
		}
	}
	relPath := strings.TrimPrefix(filepath.Clean("/"+customPath), "/")
	if relPath == "" {
		return "", fmt.Errorf("path %q resolved from pathPattern points to the NFS base path", customPath)
	}
	return relPath, nil
}

// checkNoSymlinks makes sure that no existing component of relPath below
// base is a symlink. Users can write to the directories they are given, so a
// symlink planted there could redirect a new volume, and the chmod that
// follows, to a directory that belongs to somebody else.
func checkNoSymlinks(base, relPath string) error {
	current := base
	for _, segment := range strings.Split(relPath, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %s is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("path %s is not a directory", current)
		}
	}
	return nil
}

// mountPath is where the NFS base path is mounted inside the container. It is
// a variable so that tests can point it to a temporary directory.
var mountPath = "/persistentvolumes"

var _ controller.Provisioner = &nfsProvisioner{}

func (p *nfsProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	if options.PVC.Spec.Selector != nil {
		return nil, controller.ProvisioningFinished, fmt.Errorf("claim Selector is not supported")
	}
	glog.V(4).Infof("nfs provisioner: VolumeOptions %v", options)

	pvcNamespace := options.PVC.Namespace
	pvcName := options.PVC.Name

	pvName := strings.Join([]string{pvcNamespace, pvcName, options.PVName}, "-")

	metadata := &pvcMetadata{
		data: map[string]string{
			"name":      pvcName,
			"namespace": pvcNamespace,
		},
		labels:      options.PVC.Labels,
		annotations: options.PVC.Annotations,
	}

	relPath := pvName
	pathPattern, exists := options.StorageClass.Parameters["pathPattern"]
	if exists && pathPattern != "" {
		var err error
		relPath, err = resolveCustomPath(pathPattern, metadata)
		if err != nil {
			return nil, controller.ProvisioningFinished, fmt.Errorf("invalid pathPattern for PVC %s/%s: %w", pvcNamespace, pvcName, err)
		}
	}
	path := filepath.Join(p.path, relPath)
	fullPath := filepath.Join(mountPath, relPath)

	if err := checkNoSymlinks(mountPath, relPath); err != nil {
		return nil, controller.ProvisioningFinished, fmt.Errorf("unable to provision new pv: %w", err)
	}
	glog.V(4).Infof("creating path %s", fullPath)
	if err := os.MkdirAll(fullPath, 0o777); err != nil {
		return nil, controller.ProvisioningFinished, errors.New("unable to create directory to provision new pv: " + err.Error())
	}
	// Check again to narrow the window between the first check and MkdirAll.
	if err := checkNoSymlinks(mountPath, relPath); err != nil {
		return nil, controller.ProvisioningFinished, fmt.Errorf("unable to provision new pv: %w", err)
	}
	// The directory must be writable by any UID because the pods that mount it
	// do not share the provisioner's UID. Only the leaf directory is changed.
	if err := os.Chmod(fullPath, 0o777); err != nil {
		return nil, controller.ProvisioningFinished, fmt.Errorf("unable to set permissions on %s: %w", fullPath, err)
	}

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVName,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *options.StorageClass.ReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			MountOptions:                  options.StorageClass.MountOptions,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				NFS: &v1.NFSVolumeSource{
					Server:   p.server,
					Path:     path,
					ReadOnly: false,
				},
			},
		},
	}
	return pv, controller.ProvisioningFinished, nil
}

func (p *nfsProvisioner) Delete(ctx context.Context, volume *v1.PersistentVolume) error {
	path := volume.Spec.PersistentVolumeSource.NFS.Path
	basePath := filepath.Base(path)
	oldPath := strings.Replace(path, p.path, mountPath, 1)

	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		glog.Warningf("path %s does not exist, deletion skipped", oldPath)
		return nil
	}
	if rel, err := filepath.Rel(mountPath, filepath.Clean(oldPath)); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("path %s of volume %s is outside of %s, refusing to delete it", path, volume.Name, p.path)
	}
	// Get the storage class for this volume.
	storageClass, err := p.getClassForVolume(ctx, volume)
	if err != nil {
		return err
	}

	// Determine if the "onDelete" parameter exists.
	// If it exists and has a `delete` value, delete the directory.
	// If it exists and has a `retain` value, safe the directory.
	onDelete := storageClass.Parameters["onDelete"]
	switch onDelete {
	case "delete":
		return os.RemoveAll(oldPath)
	case "retain":
		return nil
	}

	// Determine if the "archiveOnDelete" parameter exists.
	// If it exists and has a false value, delete the directory.
	// Otherwise, archive it.
	archiveOnDelete, exists := storageClass.Parameters["archiveOnDelete"]
	if exists {
		archiveBool, err := strconv.ParseBool(archiveOnDelete)
		if err != nil {
			return err
		}
		if !archiveBool {
			return os.RemoveAll(oldPath)
		}
	}

	archivePath := filepath.Join(mountPath, "archived-"+basePath)
	glog.V(4).Infof("archiving path %s to %s", oldPath, archivePath)
	return os.Rename(oldPath, archivePath)
}

// getClassForVolume returns StorageClass.
func (p *nfsProvisioner) getClassForVolume(ctx context.Context, pv *v1.PersistentVolume) (*storage.StorageClass, error) {
	if p.client == nil {
		return nil, fmt.Errorf("cannot get kube client")
	}
	className := storagehelpers.GetPersistentVolumeClass(pv)
	if className == "" {
		return nil, fmt.Errorf("volume has no storage class")
	}
	class, err := p.client.StorageV1().StorageClasses().Get(ctx, className, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return class, nil
}

func main() {
	flag.Parse()
	flag.Set("logtostderr", "true")

	server := os.Getenv("NFS_SERVER")
	if server == "" {
		glog.Fatal("NFS_SERVER not set")
	}
	path := os.Getenv("NFS_PATH")
	if path == "" {
		glog.Fatal("NFS_PATH not set")
	}
	provisionerName := os.Getenv(provisionerNameKey)
	if provisionerName == "" {
		glog.Fatalf("environment variable %s is not set! Please set it.", provisionerNameKey)
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	var config *rest.Config
	if kubeconfig != "" {
		// Create an OutOfClusterConfig and use it to create a client for the controller
		// to use to communicate with Kubernetes
		var err error
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			glog.Fatalf("Failed to create kubeconfig: %v", err)
		}
	} else {
		// Create an InClusterConfig and use it to create a client for the controller
		// to use to communicate with Kubernetes
		var err error
		config, err = rest.InClusterConfig()
		if err != nil {
			glog.Fatalf("Failed to create config: %v", err)
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}

	// The controller needs to know what the server version is because out-of-tree
	// provisioners aren't officially supported until 1.5
	serverVersion, err := clientset.Discovery().ServerVersion()
	if err != nil {
		glog.Fatalf("Error getting server version: %v", err)
	}

	leaderElection := true
	leaderElectionEnv := os.Getenv("ENABLE_LEADER_ELECTION")
	if leaderElectionEnv != "" {
		leaderElection, err = strconv.ParseBool(leaderElectionEnv)
		if err != nil {
			glog.Fatalf("Unable to parse ENABLE_LEADER_ELECTION env var: %v", err)
		}
	}

	clientNFSProvisioner := &nfsProvisioner{
		client: clientset,
		server: server,
		path:   path,
	}
	// Start the provision controller which will dynamically provision efs NFS
	// PVs
	pc := controller.NewProvisionController(clientset,
		provisionerName,
		clientNFSProvisioner,
		serverVersion.GitVersion,
		controller.LeaderElection(leaderElection),
	)
	// Never stops.
	pc.Run(context.Background())
}
