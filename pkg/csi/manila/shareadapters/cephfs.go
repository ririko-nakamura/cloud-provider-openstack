/*
Copyright 2019 The Kubernetes Authors.

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

package shareadapters

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/shares"
	"k8s.io/apimachinery/pkg/util/wait"
	manilautil "k8s.io/cloud-provider-openstack/pkg/csi/manila/util"
	"k8s.io/klog/v2"
)

type Cephfs struct{}

var _ ShareAdapter = &Cephfs{}

func (Cephfs) GetOrGrantAccess(ctx context.Context, args *GrantAccessArgs) (accessRight *shares.AccessRight, err error) {
	// First, check if the access right exists or needs to be created

	var rights []shares.AccessRight

	accessTo := args.Options.CephfsClientID
	if accessTo == "" {
		accessTo = args.Share.Name
	}

	rights, err = args.ManilaClient.GetAccessRights(ctx, args.Share.ID)
	if err != nil {
		if _, ok := err.(gophercloud.ErrResourceNotFound); !ok {
			return nil, fmt.Errorf("failed to list access rights: %v", err)
		}
	} else {
		// Try to find the access right

		for _, r := range rights {
			if r.AccessTo == accessTo && r.AccessType == "cephx" && r.AccessLevel == "rw" {
				klog.V(4).Infof("cephx access right for share %s already exists", args.Share.Name)

				accessRight = &r
				break
			}
		}
	}

	if accessRight == nil {
		// Not found, create it

		accessRight, err = args.ManilaClient.GrantAccess(ctx, args.Share.ID, shares.GrantAccessOpts{
			AccessType:  "cephx",
			AccessLevel: "rw",
			AccessTo:    accessTo,
		})

		if err != nil {
			return
		}
	}

	if accessRight.AccessKey != "" {
		// The access right is ready
		return
	}

	// Wait till a ceph key is assigned to the access right

	backoff := wait.Backoff{
		Duration: time.Second * 5,
		Factor:   1.2,
		Steps:    10,
	}

	return accessRight, wait.ExponentialBackoff(backoff, func() (bool, error) {
		rights, err := args.ManilaClient.GetAccessRights(ctx, args.Share.ID)
		if err != nil {
			return false, err
		}

		var accessRight *shares.AccessRight

		for i := range rights {
			if rights[i].AccessTo == accessTo {
				accessRight = &rights[i]
				break
			}
		}

		if accessRight == nil {
			return false, fmt.Errorf("cannot find the access right we've just created")
		}

		return accessRight.AccessKey != "", nil
	})
}

func (Cephfs) BuildVolumeContext(args *VolumeContextArgs) (volumeContext map[string]string, err error) {
	chosenExportLocationIdx, err := manilautil.FindExportLocation(args.Locations, manilautil.AnyExportLocation)
	if err != nil {
		return nil, fmt.Errorf("failed to choose an export location: %v", err)
	}

	monitors, rootPath, err := splitExportLocationPath(args.Locations[chosenExportLocationIdx].Path)

	volCtx := map[string]string{
		"monitors":        monitors,
		"rootPath":        rootPath,
		"mounter":         args.Options.CephfsMounter,
		"provisionVolume": "false",
	}

	if args.Options.CephfsKernelMountOptions != "" {
		volCtx["kernelMountOptions"] = args.Options.CephfsKernelMountOptions
	}

	if args.Options.CephfsFuseMountOptions != "" {
		volCtx["fuseMountOptions"] = args.Options.CephfsFuseMountOptions
	}

	return volCtx, err
}

func (Cephfs) BuildNodeStageSecret(args *SecretArgs) (secret map[string]string, err error) {
	return map[string]string{
		"userID":  args.AccessRight.AccessTo,
		"userKey": args.AccessRight.AccessKey,
	}, nil
}

func (Cephfs) BuildNodePublishSecret(args *SecretArgs) (secret map[string]string, err error) {
	return nil, nil
}
