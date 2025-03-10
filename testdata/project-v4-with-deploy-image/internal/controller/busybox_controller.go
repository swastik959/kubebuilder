/*
Copyright 2023 The Kubernetes authors.

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

package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	examplecomv1alpha1 "sigs.k8s.io/kubebuilder/testdata/project-v4-with-deploy-image/api/v1alpha1"
)

const busyboxFinalizer = "example.com.testproject.org/finalizer"

// Definitions to manage status conditions
const (
	// typeAvailableBusybox represents the status of the Deployment reconciliation
	typeAvailableBusybox = "Available"
	// typeDegradedBusybox represents the status used when the custom resource is deleted and the finalizer operations are must to occur.
	typeDegradedBusybox = "Degraded"
)

// BusyboxReconciler reconciles a Busybox object
type BusyboxReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// The following markers are used to generate the rules permissions (RBAC) on config/rbac using controller-gen
// when the command <make manifests> is executed.
// To know more about markers see: https://book.kubebuilder.io/reference/markers.html

//+kubebuilder:rbac:groups=example.com.testproject.org,resources=busyboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=example.com.testproject.org,resources=busyboxes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=example.com.testproject.org,resources=busyboxes/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.

// It is essential for the controller's reconciliation loop to be idempotent. By following the Operator
// pattern you will create Controllers which provide a reconcile function
// responsible for synchronizing resources until the desired state is reached on the cluster.
// Breaking this recommendation goes against the design principles of controller-runtime.
// and may lead to unforeseen consequences such as resources becoming stuck and requiring manual intervention.
// For further info:
// - About Operator Pattern: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
// - About Controllers: https://kubernetes.io/docs/concepts/architecture/controller/
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.2/pkg/reconcile
func (r *BusyboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Busybox instance
	// The purpose is check if the Custom Resource for the Kind Busybox
	// is applied on the cluster if not we return nil to stop the reconciliation
	busybox := &examplecomv1alpha1.Busybox{}
	err := r.Get(ctx, req.NamespacedName, busybox)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then, it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("busybox resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get busybox")
		return ctrl.Result{}, err
	}

	// Let's just set the status as Unknown when no status are available
	if busybox.Status.Conditions == nil || len(busybox.Status.Conditions) == 0 {
		meta.SetStatusCondition(&busybox.Status.Conditions, metav1.Condition{Type: typeAvailableBusybox, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err = r.Status().Update(ctx, busybox); err != nil {
			log.Error(err, "Failed to update Busybox status")
			return ctrl.Result{}, err
		}

		// Let's re-fetch the busybox Custom Resource after update the status
		// so that we have the latest state of the resource on the cluster and we will avoid
		// raise the issue "the object has been modified, please apply
		// your changes to the latest version and try again" which would re-trigger the reconciliation
		// if we try to update it again in the following operations
		if err := r.Get(ctx, req.NamespacedName, busybox); err != nil {
			log.Error(err, "Failed to re-fetch busybox")
			return ctrl.Result{}, err
		}
	}

	// Let's add a finalizer. Then, we can define some operations which should
	// occurs before the custom resource to be deleted.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers
	if !controllerutil.ContainsFinalizer(busybox, busyboxFinalizer) {
		log.Info("Adding Finalizer for Busybox")
		if ok := controllerutil.AddFinalizer(busybox, busyboxFinalizer); !ok {
			log.Error(err, "Failed to add finalizer into the custom resource")
			return ctrl.Result{Requeue: true}, nil
		}

		if err = r.Update(ctx, busybox); err != nil {
			log.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Check if the Busybox instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isBusyboxMarkedToBeDeleted := busybox.GetDeletionTimestamp() != nil
	if isBusyboxMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(busybox, busyboxFinalizer) {
			log.Info("Performing Finalizer Operations for Busybox before delete CR")

			// Let's add here an status "Downgrade" to define that this resource begin its process to be terminated.
			meta.SetStatusCondition(&busybox.Status.Conditions, metav1.Condition{Type: typeDegradedBusybox,
				Status: metav1.ConditionUnknown, Reason: "Finalizing",
				Message: fmt.Sprintf("Performing finalizer operations for the custom resource: %s ", busybox.Name)})

			if err := r.Status().Update(ctx, busybox); err != nil {
				log.Error(err, "Failed to update Busybox status")
				return ctrl.Result{}, err
			}

			// Perform all operations required before remove the finalizer and allow
			// the Kubernetes API to remove the custom resource.
			r.doFinalizerOperationsForBusybox(busybox)

			// TODO(user): If you add operations to the doFinalizerOperationsForBusybox method
			// then you need to ensure that all worked fine before deleting and updating the Downgrade status
			// otherwise, you should requeue here.

			// Re-fetch the busybox Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, busybox); err != nil {
				log.Error(err, "Failed to re-fetch busybox")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&busybox.Status.Conditions, metav1.Condition{Type: typeDegradedBusybox,
				Status: metav1.ConditionTrue, Reason: "Finalizing",
				Message: fmt.Sprintf("Finalizer operations for custom resource %s name were successfully accomplished", busybox.Name)})

			if err := r.Status().Update(ctx, busybox); err != nil {
				log.Error(err, "Failed to update Busybox status")
				return ctrl.Result{}, err
			}

			log.Info("Removing Finalizer for Busybox after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(busybox, busyboxFinalizer); !ok {
				log.Error(err, "Failed to remove finalizer for Busybox")
				return ctrl.Result{Requeue: true}, nil
			}

			if err := r.Update(ctx, busybox); err != nil {
				log.Error(err, "Failed to remove finalizer for Busybox")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Check if the deployment already exists, if not create a new one
	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: busybox.Name, Namespace: busybox.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new deployment
		dep, err := r.deploymentForBusybox(busybox)
		if err != nil {
			log.Error(err, "Failed to define new Deployment resource for Busybox")

			// The following implementation will update the status
			meta.SetStatusCondition(&busybox.Status.Conditions, metav1.Condition{Type: typeAvailableBusybox,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("Failed to create Deployment for the custom resource (%s): (%s)", busybox.Name, err)})

			if err := r.Status().Update(ctx, busybox); err != nil {
				log.Error(err, "Failed to update Busybox status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		log.Info("Creating a new Deployment",
			"Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err = r.Create(ctx, dep); err != nil {
			log.Error(err, "Failed to create new Deployment",
				"Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}

		// Deployment created successfully
		// We will requeue the reconciliation so that we can ensure the state
		// and move forward for the next operations
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		// Let's return the error for the reconciliation be re-trigged again
		return ctrl.Result{}, err
	}

	// The CRD API is defining that the Busybox type, have a BusyboxSpec.Size field
	// to set the quantity of Deployment instances is the desired state on the cluster.
	// Therefore, the following code will ensure the Deployment size is the same as defined
	// via the Size spec of the Custom Resource which we are reconciling.
	size := busybox.Spec.Size
	if *found.Spec.Replicas != size {
		found.Spec.Replicas = &size
		if err = r.Update(ctx, found); err != nil {
			log.Error(err, "Failed to update Deployment",
				"Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)

			// Re-fetch the busybox Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, busybox); err != nil {
				log.Error(err, "Failed to re-fetch busybox")
				return ctrl.Result{}, err
			}

			// The following implementation will update the status
			meta.SetStatusCondition(&busybox.Status.Conditions, metav1.Condition{Type: typeAvailableBusybox,
				Status: metav1.ConditionFalse, Reason: "Resizing",
				Message: fmt.Sprintf("Failed to update the size for the custom resource (%s): (%s)", busybox.Name, err)})

			if err := r.Status().Update(ctx, busybox); err != nil {
				log.Error(err, "Failed to update Busybox status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		// Now, that we update the size we want to requeue the reconciliation
		// so that we can ensure that we have the latest state of the resource before
		// update. Also, it will help ensure the desired state on the cluster
		return ctrl.Result{Requeue: true}, nil
	}

	// The following implementation will update the status
	meta.SetStatusCondition(&busybox.Status.Conditions, metav1.Condition{Type: typeAvailableBusybox,
		Status: metav1.ConditionTrue, Reason: "Reconciling",
		Message: fmt.Sprintf("Deployment for custom resource (%s) with %d replicas created successfully", busybox.Name, size)})

	if err := r.Status().Update(ctx, busybox); err != nil {
		log.Error(err, "Failed to update Busybox status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// finalizeBusybox will perform the required operations before delete the CR.
func (r *BusyboxReconciler) doFinalizerOperationsForBusybox(cr *examplecomv1alpha1.Busybox) {
	// TODO(user): Add the cleanup steps that the operator
	// needs to do before the CR can be deleted. Examples
	// of finalizers include performing backups and deleting
	// resources that are not owned by this CR, like a PVC.

	// Note: It is not recommended to use finalizers with the purpose of delete resources which are
	// created and managed in the reconciliation. These ones, such as the Deployment created on this reconcile,
	// are defined as depended of the custom resource. See that we use the method ctrl.SetControllerReference.
	// to set the ownerRef which means that the Deployment will be deleted by the Kubernetes API.
	// More info: https://kubernetes.io/docs/tasks/administer-cluster/use-cascading-deletion/

	// The following implementation will raise an event
	r.Recorder.Event(cr, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cr.Name,
			cr.Namespace))
}

// deploymentForBusybox returns a Busybox Deployment object
func (r *BusyboxReconciler) deploymentForBusybox(
	busybox *examplecomv1alpha1.Busybox) (*appsv1.Deployment, error) {
	ls := labelsForBusybox(busybox.Name)
	replicas := busybox.Spec.Size

	// Get the Operand image
	image, err := imageForBusybox()
	if err != nil {
		return nil, err
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      busybox.Name,
			Namespace: busybox.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					// TODO(user): Uncomment the following code to configure the nodeAffinity expression
					// according to the platforms which are supported by your solution. It is considered
					// best practice to support multiple architectures. build your manager image using the
					// makefile target docker-buildx. Also, you can use docker manifest inspect <image>
					// to check what are the platforms supported.
					// More info: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity
					//Affinity: &corev1.Affinity{
					//	NodeAffinity: &corev1.NodeAffinity{
					//		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					//			NodeSelectorTerms: []corev1.NodeSelectorTerm{
					//				{
					//					MatchExpressions: []corev1.NodeSelectorRequirement{
					//						{
					//							Key:      "kubernetes.io/arch",
					//							Operator: "In",
					//							Values:   []string{"amd64", "arm64", "ppc64le", "s390x"},
					//						},
					//						{
					//							Key:      "kubernetes.io/os",
					//							Operator: "In",
					//							Values:   []string{"linux"},
					//						},
					//					},
					//				},
					//			},
					//		},
					//	},
					//},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &[]bool{true}[0],
						// IMPORTANT: seccomProfile was introduced with Kubernetes 1.19
						// If you are looking for to produce solutions to be supported
						// on lower versions you must remove this option.
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Image:           image,
						Name:            "busybox",
						ImagePullPolicy: corev1.PullIfNotPresent,
						// Ensure restrictive context for the container
						// More info: https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             &[]bool{true}[0],
							AllowPrivilegeEscalation: &[]bool{false}[0],
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{
									"ALL",
								},
							},
						},
					}},
				},
			},
		},
	}

	// Set the ownerRef for the Deployment
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/
	if err := ctrl.SetControllerReference(busybox, dep, r.Scheme); err != nil {
		return nil, err
	}
	return dep, nil
}

// labelsForBusybox returns the labels for selecting the resources
// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/
func labelsForBusybox(name string) map[string]string {
	var imageTag string
	image, err := imageForBusybox()
	if err == nil {
		imageTag = strings.Split(image, ":")[1]
	}
	return map[string]string{"app.kubernetes.io/name": "Busybox",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/version":    imageTag,
		"app.kubernetes.io/part-of":    "project-v4-with-deploy-image",
		"app.kubernetes.io/created-by": "controller-manager",
	}
}

// imageForBusybox gets the Operand image which is managed by this controller
// from the BUSYBOX_IMAGE environment variable defined in the config/manager/manager.yaml
func imageForBusybox() (string, error) {
	var imageEnvVar = "BUSYBOX_IMAGE"
	image, found := os.LookupEnv(imageEnvVar)
	if !found {
		return "", fmt.Errorf("Unable to find %s environment variable with the image", imageEnvVar)
	}
	return image, nil
}

// SetupWithManager sets up the controller with the Manager.
// Note that the Deployment will be also watched in order to ensure its
// desirable state on the cluster
func (r *BusyboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&examplecomv1alpha1.Busybox{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
