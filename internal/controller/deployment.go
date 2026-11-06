package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

const (
	// DeploymentName is the name of the Deployment of a preview, in the
	// preview namespace, which holds nothing else.
	DeploymentName = "app"
	containerName  = "app"
	portName       = "http"

	// appLabel selects the pods of the preview. A Deployment's selector cannot
	// change after its creation, so it stays fixed and minimal.
	appLabel      = "app.kubernetes.io/name"
	appLabelValue = "preview-app"
)

// ensureDeployment creates or updates the Deployment of the preview in
// namespace, and returns it as read or written.
func (r *PreviewEnvironmentReconciler) ensureDeployment(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) (*appsv1.Deployment, error) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: namespace}}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, d, func() error {
		if d.ResourceVersion != "" && !ownedBy(d, preview) { // it already exists
			return fmt.Errorf("deployment %s/%s exists and was not created by Vesper for this preview",
				namespace, DeploymentName)
		}
		mutateDeployment(d, preview)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create or update deployment %s/%s: %w", namespace, DeploymentName, err)
	}
	if result != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("deployment "+string(result),
			"previewNamespace", namespace, "image", preview.Spec.Image)
	}
	return d, nil
}

// mutateDeployment sets the fields Vesper owns on d, and only those: the API
// server adds defaults to the rest (imagePullPolicy, strategy...), and
// overwriting them would make every reconciliation update the Deployment.
func mutateDeployment(d *appsv1.Deployment, preview *vesperv1alpha1.PreviewEnvironment) {
	setPreviewLabels(d, preview)

	d.Spec.Replicas = ptr.To(replicas(preview))
	if d.Spec.Selector == nil { // immutable once created
		d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: appLabelValue}}
	}

	tmpl := &d.Spec.Template
	setPreviewLabels(&tmpl.ObjectMeta, preview)
	tmpl.Labels[appLabel] = appLabelValue

	// The restricted Pod Security Standard, which the namespace enforces.
	pod := &tmpl.Spec
	pod.AutomountServiceAccountToken = ptr.To(false)
	if pod.SecurityContext == nil {
		pod.SecurityContext = &corev1.PodSecurityContext{}
	}
	pod.SecurityContext.RunAsNonRoot = ptr.To(true)
	pod.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	c := appContainer(pod)
	c.Image = preview.Spec.Image
	c.Ports = []corev1.ContainerPort{{Name: portName, ContainerPort: port(preview), Protocol: corev1.ProtocolTCP}}
	c.Env = nil
	for _, e := range preview.Spec.Env {
		c.Env = append(c.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	c.SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// appContainer returns the container of the application in pod, adding it
// if needed, so that the defaults of an existing container are kept.
func appContainer(pod *corev1.PodSpec) *corev1.Container {
	for i := range pod.Containers {
		if pod.Containers[i].Name == containerName {
			return &pod.Containers[i]
		}
	}
	pod.Containers = append(pod.Containers, corev1.Container{Name: containerName})
	return &pod.Containers[len(pod.Containers)-1]
}

// replicas and port return the spec values. The API server sets their
// defaults, so 0 only happens for an object it did not validate.
func replicas(preview *vesperv1alpha1.PreviewEnvironment) int32 {
	return max(preview.Spec.Replicas, 1)
}

func port(preview *vesperv1alpha1.PreviewEnvironment) int32 {
	if preview.Spec.Port == 0 {
		return 8080
	}
	return preview.Spec.Port
}

// deploymentAvailable reports whether d runs its latest version on every
// replica, and how many replicas are available.
func deploymentAvailable(d *appsv1.Deployment) (bool, string) {
	want := ptr.Deref(d.Spec.Replicas, 1)
	s := d.Status
	message := fmt.Sprintf("%d/%d replicas available", s.AvailableReplicas, want)
	available := s.ObservedGeneration >= d.Generation &&
		s.UpdatedReplicas == want && s.Replicas == want && s.AvailableReplicas == want
	return available, message
}
