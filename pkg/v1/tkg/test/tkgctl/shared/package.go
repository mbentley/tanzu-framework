// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint:typecheck,goconst,gocritic,stylecheck,nolintlint
package shared

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kappctrl "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/kappctrl/v1alpha1"
	kapppkgiv1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/packaging/v1alpha1"

	addonutil "github.com/vmware-tanzu/tanzu-framework/addons/pkg/util"
	runtanzuv1alpha3 "github.com/vmware-tanzu/tanzu-framework/apis/run/v1alpha3"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/tkg/constants"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/tkg/log"
)

const (
	getResourceTimeout  = time.Minute * 1
	waitForReadyTimeout = time.Minute * 20
	pollingInterval     = time.Second * 30
)

// create cluster client from kubeconfig
func createClientFromKubeconfig(exportFile string, scheme *runtime.Scheme) (client.Client, error) {
	config, err := clientcmd.LoadFromFile(exportFile)
	Expect(err).ToNot(HaveOccurred(), "Failed to load cluster Kubeconfig file from %q", exportFile)

	rawConfig, err := clientcmd.Write(*config)
	Expect(err).ToNot(HaveOccurred(), "Failed to create raw config ")

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(rawConfig)
	Expect(err).ToNot(HaveOccurred(), "Failed to create rest config ")

	client, err := client.New(restConfig, client.Options{Scheme: scheme})
	Expect(err).ToNot(HaveOccurred(), "Failed to create a cluster client")

	return client, nil
}

func checkClusterCBS(ctx context.Context, mccl, wccl client.Client, mcClusterName, wcClusterName, infrastructureName string) error {
	log.Infof("Verify addons on workload cluster %s", wcClusterName)
	var err error

	// get ClusterBootstrap and return error if not found
	clusterBootstrap := getClusterBootstrap(ctx, mccl, constants.TkgNamespace, mcClusterName)

	// verify cni package is installed on the workload cluster
	cniPkgShortName, cniPkgName, cniPkgVersion, err := getPackageDetailsFromCBS(clusterBootstrap.Spec.CNI.RefName)
	Expect(err).NotTo(HaveOccurred())
	verifyPackageInstall(ctx, wccl, wcClusterName, cniPkgShortName, cniPkgName, cniPkgVersion)

	// verify the remote kapp-controller package is installed on the management cluster
	kappPkgShortName, kappPkgName, kappPkgVersion, err := getPackageDetailsFromCBS(clusterBootstrap.Spec.Kapp.RefName)
	Expect(err).NotTo(HaveOccurred())
	verifyPackageInstall(ctx, wccl, mcClusterName, kappPkgShortName, kappPkgName, kappPkgVersion)

	// verify csi and cpi package is installed on the workload cluster if in vSphere environment
	if infrastructureName == "vsphere" {
		csiPkgShortName, csiPkgName, csiPkgVersion, err := getPackageDetailsFromCBS(clusterBootstrap.Spec.CSI.RefName)
		Expect(err).NotTo(HaveOccurred())
		cpiPkgShortName, cpiPkgName, cpiPkgVersion, err := getPackageDetailsFromCBS(clusterBootstrap.Spec.CPI.RefName)
		Expect(err).NotTo(HaveOccurred())
		verifyPackageInstall(ctx, wccl, wcClusterName, csiPkgShortName, csiPkgName, csiPkgVersion)
		verifyPackageInstall(ctx, wccl, wcClusterName, cpiPkgShortName, cpiPkgName, cpiPkgVersion)
	}

	// loop over additional packages list in clusterBootstrap spec to check package information
	if clusterBootstrap.Spec.AdditionalPackages != nil {
		// validate additional packages
		for _, additionalPkg := range clusterBootstrap.Spec.AdditionalPackages {
			pkgShortName, pkgName, pkgVersion, err := getPackageDetailsFromCBS(additionalPkg.RefName)
			Expect(err).NotTo(HaveOccurred())

			// TODO: temporarily skip verifying tkg-storageclass due to install failure issue.
			//		 this should be removed once the issue is resolved.
			if pkgShortName == "tkg-storageclass" {
				continue
			}
			verifyPackageInstall(ctx, wccl, wcClusterName, pkgShortName, pkgName, pkgVersion)
		}
	}

	return nil
}

func verifyPackageInstall(ctx context.Context, wccl client.Client, clusterName, pkgShortName, pkgName, pkgVersion string) {
	// packageInstall name for for both management and workload clusters should follow the <cluster name>-<addon short name>
	pkgiName := addonutil.GeneratePackageInstallName(clusterName, pkgShortName)
	log.Infof("Check PackageInstall %s for package %s of version %s", pkgiName, pkgName, pkgVersion)

	// verify the package is successfully deployed and its name and version match with the clusterBootstrap CR
	pkgInstall := &kapppkgiv1alpha1.PackageInstall{}
	objKey := client.ObjectKey{Namespace: constants.TkgNamespace, Name: pkgiName}
	Eventually(func() bool {
		if err := wccl.Get(ctx, objKey, pkgInstall); err != nil {
			log.Infof("Get packageinstall error: %s", err.Error())
			return false
		}
		log.Infof("Get PackageInstall, conditions: %d, %+v", len(pkgInstall.Status.GenericStatus.Conditions), pkgInstall.Status.GenericStatus)
		if len(pkgInstall.Status.GenericStatus.Conditions) == 0 {
			return false
		}
		log.Infof("%+v", pkgInstall.Status.GenericStatus.Conditions[0])
		log.Infof("%s - %s", pkgInstall.Spec.PackageRef.RefName, pkgInstall.Spec.PackageRef.VersionSelection.Constraints)
		if pkgInstall.Status.GenericStatus.Conditions[0].Type != kappctrl.ReconcileSucceeded {
			return false
		}
		if pkgInstall.Status.GenericStatus.Conditions[0].Status != corev1.ConditionTrue {
			return false
		}
		if pkgInstall.Spec.PackageRef.RefName != pkgName {
			return false
		}
		if pkgInstall.Spec.PackageRef.VersionSelection.Constraints != pkgVersion {
			return false
		}
		return true
	}, waitForReadyTimeout, pollingInterval).Should(BeTrue())
}

func getPackageDetailsFromCBS(CBSRefName string) (string, string, string, error) {
	pkgShortName := strings.Split(CBSRefName, ".")[0]

	pkgName := strings.Join(strings.Split(CBSRefName, ".")[0:4], ".")

	pkgVersion := strings.Join(strings.Split(CBSRefName, ".")[4:], ".")

	return pkgShortName, pkgName, pkgVersion, nil
}

func getClusterBootstrap(ctx context.Context, k8sClient client.Client, namespace, clusterName string) *runtanzuv1alpha3.ClusterBootstrap {
	clusterBootstrap := &runtanzuv1alpha3.ClusterBootstrap{}
	objKey := client.ObjectKey{Namespace: namespace, Name: clusterName}

	Eventually(func() error {
		return k8sClient.Get(ctx, objKey, clusterBootstrap)
	}, getResourceTimeout, pollingInterval).Should(Succeed())

	Expect(clusterBootstrap).ShouldNot(BeNil())
	return clusterBootstrap
}
