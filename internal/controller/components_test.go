// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// stackSpec is a preview of three components: a frontend, an API and a
// database (ADR-0008).
func stackSpec() vesperv1alpha1.PreviewEnvironmentSpec {
	return vesperv1alpha1.PreviewEnvironmentSpec{Components: []vesperv1alpha1.Component{
		{Name: "web", Image: "ghcr.io/acme/web:pr-42", Port: 3000, Expose: true},
		{Name: "api", Image: "ghcr.io/acme/api:pr-42",
			Env: []vesperv1alpha1.EnvVar{{Name: "DATABASE_HOST", Value: "postgres"}}},
		{Name: "postgres", Image: "postgres:17.2", Port: 5432},
	}}
}

// TestEnvtestAcceptsComponents creates a preview of three components: the
// API server accepts it and fills the defaults of each component.
func TestEnvtestAcceptsComponents(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, stackSpec())

	var preview vesperv1alpha1.PreviewEnvironment
	if err := p.get(&preview); err != nil {
		t.Fatalf("get preview: %v", err)
	}
	got := preview.Spec.Components
	if len(got) != 3 {
		t.Fatalf("components = %+v, want 3", got)
	}
	for _, c := range []struct {
		i              int
		name           string
		port, replicas int32
	}{{0, "web", 3000, 1}, {1, "api", 8080, 1}, {2, "postgres", 5432, 1}} {
		if got[c.i].Name != c.name || got[c.i].Port != c.port || got[c.i].Replicas != c.replicas {
			t.Errorf("component %d = %+v, want %s with port %d and %d replica", c.i, got[c.i], c.name, c.port, c.replicas)
		}
	}
	// The top-level defaults are still filled in, and allowed next to
	// components, since they are only the defaults.
	if preview.Spec.Port != 8080 || preview.Spec.Replicas != 1 {
		t.Errorf("top-level port %d, replicas %d; want the defaults 8080 and 1", preview.Spec.Port, preview.Spec.Replicas)
	}
}

// TestEnvtestComponentsValidation sends invalid previews with components: the
// API server rejects them with the rules of the CRD schema.
func TestEnvtestComponentsValidation(t *testing.T) {
	requireEnvtest(t)
	namespace := newUserNamespace(t)

	tests := []struct {
		name    string
		mutate  func(*vesperv1alpha1.PreviewEnvironmentSpec)
		message string // part of the error message
	}{
		{"neither image nor components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components = nil
		}, "give image, for one application, or components"},
		{"image and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Image = "ghcr.io/acme/app:pr-42"
		}, "not at the top of spec"},
		{"env and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Env = []vesperv1alpha1.EnvVar{{Name: "LOG_LEVEL"}}
		}, "not at the top of spec"},
		{"port and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Port = 3000
		}, "not at the top of spec"},
		{"replicas and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Replicas = 2
		}, "not at the top of spec"},
		{"no component exposed", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[0].Expose = false
		}, "exactly one component must have expose: true"},
		{"two components exposed", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Expose = true
		}, "exactly one component must have expose: true"},
		{"duplicate names", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Name = "web"
		}, "spec.components[1]"},
		{"too many components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			for i := len(s.Components); i <= vesperv1alpha1.MaxComponents; i++ {
				s.Components = append(s.Components,
					vesperv1alpha1.Component{Name: fmt.Sprintf("worker-%d", i), Image: "ghcr.io/acme/worker:1"})
			}
		}, "spec.components"},
		{"name not a DNS label", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Name = "API"
		}, "spec.components[1].name"},
		{"name starts with a digit", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Name = "1api"
		}, "spec.components[1].name"},
		{"latest tag", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[2].Image = "postgres:latest"
		}, "spec.components[2].image"},
		{"no tag", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[2].Image = "postgres"
		}, "spec.components[2].image"},
		{"port too high", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[0].Port = 70000
		}, "spec.components[0].port"},
		{"too many replicas", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[0].Replicas = 6
		}, "spec.components[0].replicas"},
		{"bad env name", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Env = []vesperv1alpha1.EnvVar{{Name: "1BAD"}}
		}, "spec.components[1].env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := stackSpec()
			tt.mutate(&spec)
			preview := &vesperv1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "pr-1"},
				Spec:       spec,
			}
			err := envClient.Create(context.Background(), preview)
			if !apierrors.IsInvalid(err) {
				t.Fatalf("create: %v, want an Invalid error", err)
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error %q does not contain %q", err, tt.message)
			}
		})
	}
}

// newStackPreview returns newPreview with the components of stackSpec, and
// the defaults the API server would fill in.
func newStackPreview() *vesperv1alpha1.PreviewEnvironment {
	preview := newPreview()
	preview.Spec = stackSpec()
	for i := range preview.Spec.Components {
		c := &preview.Spec.Components[i]
		if c.Port == 0 {
			c.Port = 8080
		}
		c.Replicas = 1
	}
	return preview
}

// TestReconcileRunsComponents checks the objects of a preview of three
// components: a Deployment and a Service per component, each Service in
// front of the pods of its component only, on the port of the component.
func TestReconcileRunsComponents(t *testing.T) {
	r := newReconciler(t, newStackPreview())
	r.PreviewDomain = testDomain
	reconcileOnce(t, r)
	nsName := NamespaceName("team-a", "pr-42")

	for _, c := range []struct {
		name  string
		image string
		port  int32
	}{{"web", "ghcr.io/acme/web:pr-42", 3000}, {"api", "ghcr.io/acme/api:pr-42", 8080}, {"postgres", "postgres:17.2", 5432}} {
		var d appsv1.Deployment
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: nsName, Name: c.name}, &d); err != nil {
			t.Fatalf("get deployment %s: %v", c.name, err)
		}
		selector := map[string]string{LabelComponent: c.name}
		if !selectorIs(&d, selector) {
			t.Errorf("deployment %s selector = %v, want %v", c.name, d.Spec.Selector, selector)
		}
		podLabels := previewLabels(newPreview())
		maps.Copy(podLabels, selector)
		if !maps.Equal(d.Spec.Template.Labels, podLabels) {
			t.Errorf("deployment %s pod labels = %v, want %v", c.name, d.Spec.Template.Labels, podLabels)
		}
		containers := d.Spec.Template.Spec.Containers
		if len(containers) != 1 || containers[0].Name != c.name || containers[0].Image != c.image ||
			containers[0].Ports[0].ContainerPort != c.port {
			t.Errorf("deployment %s containers = %+v, want %s with %s on port %d", c.name, containers, c.name, c.image, c.port)
		}
		if d.Spec.Template.Spec.SecurityContext == nil || !ptr.Deref(d.Spec.Template.Spec.SecurityContext.RunAsNonRoot, false) {
			t.Errorf("deployment %s does not run as non-root", c.name)
		}

		var s corev1.Service
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: nsName, Name: c.name}, &s); err != nil {
			t.Fatalf("get service %s: %v", c.name, err)
		}
		if !maps.Equal(s.Spec.Selector, selector) {
			t.Errorf("service %s selector = %v, want %v", c.name, s.Spec.Selector, selector)
		}
		if len(s.Spec.Ports) != 1 || s.Spec.Ports[0].Port != c.port || s.Spec.Ports[0].TargetPort != intstr.FromString("http") {
			t.Errorf("service %s ports = %+v, want %d to http", c.name, s.Spec.Ports, c.port)
		}
	}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: nsName, Name: DeploymentName}, &appsv1.Deployment{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("get deployment %s: %v, want NotFound", DeploymentName, err)
	}

	ing, err := getIngress(t, r)
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if got := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name; got != "web" {
		t.Errorf("ingress backend = %q, want the exposed component web", got)
	}
	p := getPreview(t, r)
	if want := "http://web." + nsName + ".svc.cluster.local:3000"; p.Status.InternalURL != want {
		t.Errorf("internalURL = %q, want %q", p.Status.InternalURL, want)
	}
	ready := meta.FindStatusCondition(p.Status.Conditions, vesperv1alpha1.ConditionReady)
	if ready == nil || ready.Reason != vesperv1alpha1.ReasonProvisioning || ready.Message != "component web: 0/1 replicas available" {
		t.Errorf("Ready condition = %+v, want Provisioning, component web: 0/1 replicas available", ready)
	}
}

func TestComponentsCondition(t *testing.T) {
	deployment := func(name string, available bool, conditions ...appsv1.DeploymentCondition) *appsv1.Deployment {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, Generation: 1}}
		d.Spec.Replicas = ptr.To[int32](1)
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Conditions: conditions}
		if available {
			d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas = 1, 1, 1
		}
		return d
	}
	quota := appsv1.DeploymentCondition{
		Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Message: "exceeded quota",
	}
	tests := []struct {
		name        string
		deployments []*appsv1.Deployment
		want        condition
		wantPhase   vesperv1alpha1.Phase
	}{
		{"one component keeps the messages of one Deployment", []*appsv1.Deployment{deployment("app", true)},
			condition{metav1.ConditionTrue, "DeploymentAvailable", "1/1 replicas available"}, "Ready"},
		{"all available", []*appsv1.Deployment{deployment("web", true), deployment("api", true)},
			condition{metav1.ConditionTrue, "DeploymentAvailable", "2/2 components available"}, "Ready"},
		{"the first one not available", []*appsv1.Deployment{deployment("web", true), deployment("api", false), deployment("db", false)},
			condition{metav1.ConditionFalse, "Provisioning", "component api: 0/1 replicas available"}, "Pending"},
		{"a failure before a pending one", []*appsv1.Deployment{deployment("web", false), deployment("api", false, quota)},
			condition{metav1.ConditionFalse, "PodsRejected", "component api: exceeded quota"}, "Failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, phase := componentsCondition(tt.deployments, nil, nil)
			if got != tt.want || phase != tt.wantPhase {
				t.Errorf("componentsCondition = %+v, %s, want %+v, %s", got, phase, tt.want, tt.wantPhase)
			}
		})
	}
}

// TestComponentsConditionKeepsPullCause: while the kubelet waits before it
// pulls again, the cause of the last pull of the same component is kept,
// and the one of another component is not reused.
func TestComponentsConditionKeepsPullCause(t *testing.T) {
	api := testDeployment(currentImage)
	api.Name = "api"
	api.Spec.Selector.MatchLabels = map[string]string{LabelComponent: "api"}
	api.Spec.Template.Spec.Containers[0].Name = "api"
	web := testDeployment(currentImage)
	web.Name = "web"
	web.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	pod := testPod("api-1", currentImage, "ImagePullBackOff", "Back-off pulling image")
	pod.Labels = map[string]string{LabelComponent: "api"}
	pod.Spec.Containers[0].Name, pod.Status.ContainerStatuses[0].Name = "api", "api"

	for _, tt := range []struct{ previous, want string }{
		{"component api: pod api-1 cannot pull: not found", "component api: pod api-1 cannot pull: not found"},
		{"component web: pod web-1 cannot pull: not found", "component api: pod api-1 cannot pull image " + currentImage + ": Back-off pulling image"},
	} {
		previous := &metav1.Condition{Reason: vesperv1alpha1.ReasonImagePullFailed, Message: tt.previous}
		got, _ := componentsCondition([]*appsv1.Deployment{web, api}, []corev1.Pod{pod}, previous)
		if got.reason != vesperv1alpha1.ReasonImagePullFailed || got.message != tt.want {
			t.Errorf("after %q: condition = %+v, want ImagePullFailed, %q", tt.previous, got, tt.want)
		}
	}
}

// setAvailable writes the status the Deployment controller would write once
// the pods of the Deployment name run.
func (p envPreview) setAvailable(t *testing.T, name string) {
	t.Helper()
	eventually(t, func() error {
		var d appsv1.Deployment
		if err := p.getInPreviewNamespace(name, &d); err != nil {
			return err
		}
		d.Status = appsv1.DeploymentStatus{
			ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
		}
		return envClient.Status().Update(context.Background(), &d) // retried on a conflict
	})
}

// updateSpec changes the spec of the preview, retrying on a conflict.
func (p envPreview) updateSpec(t *testing.T, mutate func(*vesperv1alpha1.PreviewEnvironmentSpec)) {
	t.Helper()
	eventually(t, func() error {
		var preview vesperv1alpha1.PreviewEnvironment
		if err := p.get(&preview); err != nil {
			return err
		}
		mutate(&preview.Spec)
		return envClient.Update(context.Background(), &preview)
	})
}

// waitForComponents waits until the preview namespace holds a Deployment and
// a Service for each name, and no other, and returns the Deployments by name.
func (p envPreview) waitForComponents(t *testing.T, names ...string) map[string]appsv1.Deployment {
	t.Helper()
	deployments := map[string]appsv1.Deployment{}
	eventually(t, func() error {
		var ds appsv1.DeploymentList
		var ss corev1.ServiceList
		for _, list := range []client.ObjectList{&ds, &ss} {
			if err := envClient.List(context.Background(), list, client.InNamespace(p.previewNamespace)); err != nil {
				return err
			}
		}
		clear(deployments)
		var dNames, sNames []string
		for _, d := range ds.Items {
			if d.DeletionTimestamp == nil {
				dNames = append(dNames, d.Name)
				deployments[d.Name] = d
			}
		}
		for _, s := range ss.Items {
			sNames = append(sNames, s.Name)
		}
		want := slices.Sorted(slices.Values(names))
		if slices.Compare(slices.Sorted(slices.Values(dNames)), want) != 0 ||
			slices.Compare(slices.Sorted(slices.Values(sNames)), want) != 0 {
			return fmt.Errorf("deployments %v, services %v, want %v", dNames, sNames, want)
		}
		return nil
	})
	return deployments
}

// TestEnvtestRunsComponents runs a preview of three components: it is Ready
// once the Deployments of all three are available.
func TestEnvtestRunsComponents(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, stackSpec())
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	p.waitForComponents(t, "web", "api", "postgres")

	p.setAvailable(t, "web")
	p.setAvailable(t, "api")
	preview := p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	eventually(t, func() error {
		if err := p.get(preview); err != nil {
			return err
		}
		ready := meta.FindStatusCondition(preview.Status.Conditions, vesperv1alpha1.ConditionReady)
		if ready.Message != "component postgres: 0/1 replicas available" {
			return fmt.Errorf("Ready message = %q, want the one of postgres", ready.Message)
		}
		return nil
	})

	p.setAvailable(t, "postgres")
	preview = p.waitForStatus(t, vesperv1alpha1.PhaseReady, vesperv1alpha1.ReasonDeploymentAvailable)
	ready := meta.FindStatusCondition(preview.Status.Conditions, vesperv1alpha1.ConditionReady)
	if ready.Message != "3/3 components available" {
		t.Errorf("Ready message = %q, want 3/3 components available", ready.Message)
	}
}

// TestEnvtestRemovesComponents renames a component and removes another: their
// Deployments and Services are deleted, and a Deployment Vesper did not
// create is left alone.
func TestEnvtestRemovesComponents(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, stackSpec())
	p.waitForComponents(t, "web", "api", "postgres")

	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.previewNamespace, Name: "foreign"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foreign"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foreign"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox:1.37"}}},
			},
		},
	}
	if err := envClient.Create(context.Background(), foreign); err != nil {
		t.Fatalf("create foreign deployment: %v", err)
	}

	p.updateSpec(t, func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
		s.Components[1].Name = "backend"
		s.Components = s.Components[:2]
	})
	eventually(t, func() error {
		var ds appsv1.DeploymentList
		if err := envClient.List(context.Background(), &ds, client.InNamespace(p.previewNamespace),
			client.MatchingLabels(map[string]string{LabelManagedBy: ManagedByVesper})); err != nil {
			return err
		}
		var names []string
		for _, d := range ds.Items {
			if d.DeletionTimestamp == nil {
				names = append(names, d.Name)
			}
		}
		if slices.Compare(slices.Sorted(slices.Values(names)), []string{"backend", "web"}) != 0 {
			return fmt.Errorf("deployments of Vesper %v, want backend and web", names)
		}
		return nil
	})
	var ss corev1.ServiceList
	eventually(t, func() error {
		if err := envClient.List(context.Background(), &ss, client.InNamespace(p.previewNamespace)); err != nil {
			return err
		}
		if len(ss.Items) != 2 {
			return fmt.Errorf("%d services, want 2", len(ss.Items))
		}
		return nil
	})
	if err := p.getInPreviewNamespace("foreign", &appsv1.Deployment{}); err != nil {
		t.Errorf("get foreign deployment: %v, want it kept", err)
	}
}

// TestEnvtestSwitchesForms switches a preview from one image to components,
// one of them named app, and back. The Deployment app changes its selector,
// which cannot be updated: it is created again.
func TestEnvtestSwitchesForms(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	before := p.waitForComponents(t, "app")["app"]

	p.updateSpec(t, func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
		*s = vesperv1alpha1.PreviewEnvironmentSpec{TTL: s.TTL, Components: []vesperv1alpha1.Component{
			{Name: "app", Image: "ghcr.io/acme/app:pr-42", Expose: true},
			{Name: "worker", Image: "ghcr.io/acme/worker:pr-42"},
		}}
	})
	var app appsv1.Deployment
	eventually(t, func() error {
		app = p.waitForComponents(t, "app", "worker")["app"]
		if app.UID == before.UID || !selectorIs(&app, map[string]string{LabelComponent: "app"}) {
			return errors.New("deployment app not created again with the selector of its component")
		}
		return nil
	})
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	p.updateSpec(t, func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
		*s = vesperv1alpha1.PreviewEnvironmentSpec{TTL: s.TTL, Image: "ghcr.io/acme/app:pr-43"}
	})
	eventually(t, func() error {
		back := p.waitForComponents(t, "app")["app"]
		if back.UID == app.UID || !selectorIs(&back, map[string]string{appLabel: appLabelValue}) {
			return errors.New("deployment app not created again with the selector of one image")
		}
		return nil
	})
	preview := p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	if want := "http://app." + p.previewNamespace + ".svc.cluster.local"; preview.Status.InternalURL != want {
		t.Errorf("internalURL = %q, want %q", preview.Status.InternalURL, want)
	}
}

// TestEnvtestKeepsV02Objects creates the objects of a preview as Vesper v0.2
// did, then the preview: the upgraded operator changes none of them, so no
// pod restarts.
func TestEnvtestKeepsV02Objects(t *testing.T) {
	requireEnvtest(t)
	userNamespace := newUserNamespace(t)
	preview := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: userNamespace, Name: "pr-42"},
		Spec:       testSpec,
	}
	p := envPreview{namespace: userNamespace, name: "pr-42", previewNamespace: NamespaceName(userNamespace, "pr-42")}
	labels := previewLabels(preview)
	nsLabels := maps.Clone(labels)
	nsLabels[labelPodSecurity] = "restricted"
	podLabels := maps.Clone(labels)
	podLabels[appLabel] = appLabelValue
	v02 := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.previewNamespace, Labels: nsLabels}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.previewNamespace, Name: "app", Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](1),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: appLabelValue}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr.To(false),
						SecurityContext: &corev1.PodSecurityContext{
							RunAsNonRoot:   ptr.To(true),
							SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Containers: []corev1.Container{{
							Name:  "app",
							Image: testSpec.Image,
							Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
						}},
					},
				},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.previewNamespace, Name: "app", Labels: labels},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeClusterIP,
				Selector: map[string]string{appLabel: appLabelValue},
				Ports: []corev1.ServicePort{{
					Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, TargetPort: intstr.FromString("http"),
				}},
			},
		},
	}
	for _, obj := range v02 {
		if err := envClient.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %T: %v", obj, err)
		}
	}
	d, s := v02[1], v02[2]
	if err := envClient.Create(context.Background(), preview); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	for _, obj := range []client.Object{d, s} {
		before := obj.GetResourceVersion()
		if err := p.getInPreviewNamespace("app", obj); err != nil {
			t.Fatalf("get %T: %v", obj, err)
		}
		if obj.GetResourceVersion() != before {
			t.Errorf("%T written by the upgraded operator: resourceVersion %s, then %s",
				obj, before, obj.GetResourceVersion())
		}
	}
}
