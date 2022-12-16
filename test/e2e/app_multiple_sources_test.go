package e2e

import (
	"testing"

	. "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	. "github.com/argoproj/argo-cd/v2/test/e2e/fixture"
	. "github.com/argoproj/argo-cd/v2/test/e2e/fixture/app"
	. "github.com/argoproj/argo-cd/v2/util/argo"

	// . "github.com/argoproj/argo-cd/v2/util/errors"
	"github.com/stretchr/testify/assert"
)

func TestMultiSourceAppCreation(t *testing.T) {
	sources := []ApplicationSource{{
		RepoURL: RepoURL(RepoURLTypeFile),
		Path:    guestbookPath,
	}, {
		RepoURL: RepoURL(RepoURLTypeFile),
		Path:    "two-nice-pods",
	}}
	ctx := Given(t)
	ctx.
		Sources(sources).
		When().
		CreateMultiSourceAppFromFile().
		Then().
		Expect(SyncStatusIs(SyncStatusCodeOutOfSync)).
		And(func(app *Application) {
			assert.Equal(t, Name(), app.Name)
			for i, source := range app.Spec.GetSources() {
				assert.Equal(t, sources[i].RepoURL, source.RepoURL)
				assert.Equal(t, sources[i].Path, source.Path)
			}
			assert.Equal(t, DeploymentNamespace(), app.Spec.Destination.Namespace)
			assert.Equal(t, KubernetesInternalAPIServerAddr, app.Spec.Destination.Server)
		}).
		Expect(Event(EventReasonResourceCreated, "create")).
		And(func(_ *Application) {
			// app should be listed
			output, err := RunCli("app", "list")
			assert.NoError(t, err)
			assert.Contains(t, output, Name())
		}).
		Expect(Success("")).
		And(func(app *Application) {
			statusByName := map[string]SyncStatusCode{}
			for _, r := range app.Status.Resources {
				statusByName[r.Name] = r.Status
			}
			assert.Equal(t, SyncStatusCodeOutOfSync, statusByName["pod-1"])
			assert.Equal(t, SyncStatusCodeOutOfSync, statusByName["pod-2"])
			// check if the app has 3 resources, guestbook and 2 pods
			assert.Len(t, statusByName, 3)
		})
}

func TestMultiSourceAppCreationWithHelmExternalValueFiles(t *testing.T) {
	sources := []ApplicationSource{{
		RepoURL: RepoURL(RepoURLTypeFile),
		Path:    "multiple-source-values",
		Ref:     "values",
	}, {
		RepoURL:        "https://github.com/argoproj/argocd-example-apps.git",
		TargetRevision: "HEAD",
		Path:           "helm-guestbook",
		Helm: &ApplicationSourceHelm{
			ValueFiles: []string{
				"$values/values.yaml",
			},
		},
	}}
	ctx := Given(t)
	ctx.
		Sources(sources).
		When().
		CreateMultiSourceAppFromFile().
		Then().
		Expect(SyncStatusIs(SyncStatusCodeOutOfSync)).
		And(func(app *Application) {
			assert.Equal(t, Name(), app.Name)
			for i, source := range app.Spec.GetSources() {
				assert.Equal(t, sources[i].RepoURL, source.RepoURL)
				assert.Equal(t, sources[i].Path, source.Path)
			}
			assert.Equal(t, DeploymentNamespace(), app.Spec.Destination.Namespace)
			assert.Equal(t, KubernetesInternalAPIServerAddr, app.Spec.Destination.Server)
		}).
		Expect(Event(EventReasonResourceCreated, "create")).
		And(func(_ *Application) {
			// app should be listed
			output, err := RunCli("app", "list")
			assert.NoError(t, err)
			assert.Contains(t, output, Name())
		}).
		Expect(Success("")).
		And(func(app *Application) {
			//Verify delete app does not delete the namespace auto created
			output, err := Run("", "kubectl", "get", "replicas", AppNamespace())
			assert.NoError(t, err)
			assert.Contains(t, output, 2)
		})

}
