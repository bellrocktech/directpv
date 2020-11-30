// This file is part of MinIO Direct CSI
// Copyright (c) 2020 MinIO, Inc.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/minio/direct-csi/pkg/apis/direct.csi.min.io/v1alpha1"
	"github.com/minio/direct-csi/pkg/clientset"
	"github.com/minio/direct-csi/pkg/listener"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/golang/glog"
	kubeclientset "k8s.io/client-go/kubernetes"
)

type DirectCSIDriveListener struct {
	kubeClient      kubeclientset.Interface
	directcsiClient clientset.Interface
	nodeID          string
}

func (b *DirectCSIDriveListener) InitializeKubeClient(k kubeclientset.Interface) {
	b.kubeClient = k
}

func (b *DirectCSIDriveListener) InitializeDirectCSIClient(bc clientset.Interface) {
	b.directcsiClient = bc
}

func (b *DirectCSIDriveListener) Add(ctx context.Context, obj *v1alpha1.DirectCSIDrive) error {
	glog.V(1).Infof("add called for DirectCSIDrive %s", obj.Name)
	return nil
}

func (b *DirectCSIDriveListener) Update(ctx context.Context, old, new *v1alpha1.DirectCSIDrive) error {

	directCSIClient := b.directcsiClient.DirectV1alpha1()
	var uErr error

	if b.nodeID != new.OwnerNode {
		glog.V(5).Infof("Skipping drive %s", new.ObjectMeta.Name)
		return nil
	}

	// Do not process the request if satisfied already
	if !new.DirectCSIOwned {
		return nil
	}

	if new.RequestedFormat.Filesystem == "" && new.RequestedFormat.Mountpoint == "" {
		return nil
	}

	if new.DriveStatus == v1alpha1.Online {
		glog.Errorf("Cannot format a drive in use %s", new.ObjectMeta.Name)
		return nil
	}

	fsType := new.RequestedFormat.Filesystem
	if fsType != "" {
		isForceOptionSet := new.RequestedFormat.Force
		if new.Mountpoint != "" {
			if !isForceOptionSet {
				glog.Errorf("Cannot format mounted drive - %s. Set 'force: true' to override", new.ObjectMeta.Name)
				return nil
			}
			// Get absolute path
			abMountPath, fErr := filepath.Abs(new.Mountpoint)
			if fErr != nil {
				return fErr
			}
			// Check and unmount if the drive is already mounted
			if err := UnmountIfMounted(abMountPath); err != nil {
				return err
			}
			// Update the truth immediately that the drive is been unmounted (OR) the drive does not have a mountpoint
			new.Mountpoint = ""
			if new, uErr = directCSIClient.DirectCSIDrives().Update(ctx, new, metav1.UpdateOptions{}); uErr != nil {
				return uErr
			}
		}
		if new.Filesystem != "" && !isForceOptionSet {
			glog.Errorf("Drive already has a filesystem - %s", new.ObjectMeta.Name)
			return nil
		}
		if fErr := FormatDevice(ctx, new.Path, fsType, isForceOptionSet); fErr != nil {
			return fmt.Errorf("Failed to format the device: %v", fErr)
		}

		// Update the truth immediately that the drive is been unmounted (OR) the drive does not have a mountpoint
		new.Filesystem = fsType
		new.DriveStatus = v1alpha1.New
		new.RequestedFormat.Filesystem = ""
		new.Mountpoint = ""
		new.MountOptions = []string{}
		if new, uErr = directCSIClient.DirectCSIDrives().Update(ctx, new, metav1.UpdateOptions{}); uErr != nil {
			return uErr
		}
	}

	if new.Mountpoint == "" {
		mountPoint := new.RequestedFormat.Mountpoint
		if mountPoint == "" {
			mountPoint = filepath.Join(string(filepath.Separator), "mnt", "direct-csi", new.ObjectMeta.Name)
		}

		if err := MountDevice(new.Path, mountPoint, fsType, new.RequestedFormat.Mountoptions); err != nil {
			return fmt.Errorf("Failed to mount the device: %v", err)
		}

		new.RequestedFormat.Force = false
		new.Mountpoint = mountPoint
		new.RequestedFormat.Mountpoint = ""
		new.RequestedFormat.Mountoptions = []string{}
		stat := &syscall.Statfs_t{}
		if err := syscall.Statfs(new.Mountpoint, stat); err != nil {
			return err
		}
		availBlocks := int64(stat.Bavail)
		new.FreeCapacity = int64(stat.Bsize) * availBlocks

		if new, uErr = directCSIClient.DirectCSIDrives().Update(ctx, new, metav1.UpdateOptions{}); uErr != nil {
			return uErr
		}
	}

	glog.V(4).Infof("Successfully added DirectCSIDrive %s", new.ObjectMeta.Name)
	return nil
}

func (b *DirectCSIDriveListener) Delete(ctx context.Context, obj *v1alpha1.DirectCSIDrive) error {
	return nil
}

func startController(ctx context.Context, nodeID string) error {
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	ctrl, err := listener.NewDefaultDirectCSIController("node-controller", hostname, 40)
	if err != nil {
		glog.Error(err)
		return err
	}
	ctrl.AddDirectCSIDriveListener(&DirectCSIDriveListener{nodeID: nodeID})
	return ctrl.Run(ctx)
}