package promotions

import (
	"context"
	"fmt"
	"strings"

	argocd "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/internal/logging"
)

const authorizedEnvAnnotationKey = "kargo.akuity.io/authorized-env"

func (r *reconciler) applyArgoCDAppUpdate(
	ctx context.Context,
	envMeta metav1.ObjectMeta,
	newState api.EnvironmentState,
	update api.ArgoCDAppUpdate,
) error {
	app, err :=
		r.getArgoCDAppFn(ctx, r.client, update.AppNamespace, update.AppName)
	if err != nil {
		return errors.Wrapf(
			err,
			"error finding Argo CD Application %q in namespace %q",
			update.AppName,
			update.AppNamespace,
		)
	}
	if app == nil {
		return errors.Errorf(
			"unable to find Argo CD Application %q in namespace %q",
			update.AppName,
			update.AppNamespace,
		)
	}

	// Make sure this is allowed!
	if err = r.authorizeArgoCDAppUpdate(envMeta, app); err != nil {
		return err
	}

	patch := client.MergeFrom(app.DeepCopy())

	for _, srcUpdate := range update.SourceUpdates {
		if app.Spec.Source != nil {
			var source argocd.ApplicationSource
			source, err = r.applyArgoCDSourceUpdateFn(
				*app.Spec.Source,
				newState,
				srcUpdate,
			)
			if err != nil {
				return errors.Wrapf(
					err,
					"error updating source of Argo CD Application %q in namespace %q",
					update.AppName,
					update.AppNamespace,
				)
			}
			app.Spec.Source = &source
		}
		for i, source := range app.Spec.Sources {
			if source, err = r.applyArgoCDSourceUpdateFn(
				source,
				newState,
				srcUpdate,
			); err != nil {
				return errors.Wrapf(
					err,
					"error updating source(s) of Argo CD Application %q in namespace %q",
					update.AppName,
					update.AppNamespace,
				)
			}
			app.Spec.Sources[i] = source
		}
	}

	if app.ObjectMeta.Annotations == nil {
		app.ObjectMeta.Annotations = map[string]string{}
	}
	app.ObjectMeta.Annotations[argocd.AnnotationKeyRefresh] =
		string(argocd.RefreshTypeHard)
	app.Operation = &argocd.Operation{
		Sync: &argocd.SyncOperation{
			Revisions: []string{},
		},
	}
	if app.Spec.Source != nil {
		app.Operation.Sync.Revisions = []string{app.Spec.Source.TargetRevision}
	}
	for _, source := range app.Spec.Sources {
		app.Operation.Sync.Revisions =
			append(app.Operation.Sync.Revisions, source.TargetRevision)
	}

	if err = r.patchFn(ctx, app, patch, &client.PatchOptions{}); err != nil {
		return errors.Wrapf(err, "error patching Argo CD Application %q", app.Name)
	}

	logging.LoggerFromContext(ctx).WithField("app", app.Name).
		Debug("patched Argo CD Application")

	return nil
}

func (r *reconciler) authorizeArgoCDAppUpdate(
	envMeta metav1.ObjectMeta,
	app *argocd.Application,
) error {
	permErr := errors.Errorf(
		"Argo CD Application %q in namespace %q does not permit mutation by "+
			"Kargo Environment %s in namespace %s",
		app.Name,
		app.Namespace,
		envMeta.Name,
		envMeta.Namespace,
	)
	if app.Annotations == nil {
		return permErr
	}
	allowedEnv, ok := app.Annotations[authorizedEnvAnnotationKey]
	if !ok {
		return permErr
	}
	tokens := strings.SplitN(allowedEnv, ":", 2)
	if len(tokens) != 2 {
		return errors.Errorf(
			"unable to parse value of annotation %q (%q) on Argo CD Application "+
				"%q in namespace %q",
			authorizedEnvAnnotationKey,
			allowedEnv,
			app.Name,
			app.Namespace,
		)
	}
	allowedNamespace, allowedName := tokens[0], tokens[1]
	if envMeta.Namespace != allowedNamespace || envMeta.Name != allowedName {
		return permErr
	}
	return nil
}

func (r *reconciler) applyArgoCDSourceUpdate(
	source argocd.ApplicationSource,
	newState api.EnvironmentState,
	update api.ArgoCDSourceUpdate,
) (argocd.ApplicationSource, error) {
	if source.RepoURL != update.RepoURL || source.Chart != update.Chart {
		return source, nil
	}

	if update.UpdateTargetRevision {
		var done bool
		for _, commit := range newState.Commits {
			if commit.RepoURL == source.RepoURL {
				source.TargetRevision = commit.ID
				done = true
				break
			}
		}
		if !done {
			for _, chart := range newState.Charts {
				if chart.RegistryURL == source.RepoURL && chart.Name == source.Chart {
					source.TargetRevision = chart.Version
					break
				}
			}
		}
	}

	if update.Kustomize != nil && len(update.Kustomize.Images) > 0 {
		if source.Kustomize == nil {
			source.Kustomize = &argocd.ApplicationSourceKustomize{}
		}
		source.Kustomize.Images = buildKustomizeImagesForArgoCDAppSource(
			newState.Images,
			update.Kustomize.Images,
		)
	}

	if update.Helm != nil && len(update.Helm.Images) > 0 {
		if source.Helm == nil {
			source.Helm = &argocd.ApplicationSourceHelm{}
		}
		if source.Helm.Parameters == nil {
			source.Helm.Parameters = []argocd.HelmParameter{}
		}
		changes := buildHelmParamChangesForArgoCDAppSource(
			newState.Images,
			update.Helm.Images,
		)
	imageUpdateLoop:
		for k, v := range changes {
			newParam := argocd.HelmParameter{
				Name:  k,
				Value: v,
			}
			for i, param := range source.Helm.Parameters {
				if param.Name == k {
					source.Helm.Parameters[i] = newParam
					continue imageUpdateLoop
				}
			}
			source.Helm.Parameters = append(source.Helm.Parameters, newParam)
		}
	}

	return source, nil
}

func buildKustomizeImagesForArgoCDAppSource(
	images []api.Image,
	imageUpdates []string,
) argocd.KustomizeImages {
	tagsByImage := map[string]string{}
	for _, image := range images {
		tagsByImage[image.RepoURL] = image.Tag
	}
	kustomizeImages := argocd.KustomizeImages{}
	for _, imageUpdate := range imageUpdates {
		tag, found := tagsByImage[imageUpdate]
		if !found {
			// There's no change to make in this case.
			continue
		}
		kustomizeImages = append(
			kustomizeImages,
			argocd.KustomizeImage(
				fmt.Sprintf("%s=%s:%s", imageUpdate, imageUpdate, tag),
			),
		)
	}
	return kustomizeImages
}

func buildHelmParamChangesForArgoCDAppSource(
	images []api.Image,
	imageUpdates []api.ArgoCDHelmImageUpdate,
) map[string]string {
	tagsByImage := map[string]string{}
	for _, image := range images {
		tagsByImage[image.RepoURL] = image.Tag
	}
	changes := map[string]string{}
	for _, imageUpdate := range imageUpdates {
		if imageUpdate.Value != api.ImageUpdateValueTypeImage &&
			imageUpdate.Value != api.ImageUpdateValueTypeTag {
			// This really shouldn't happen, so we'll ignore it.
			continue
		}
		tag, found := tagsByImage[imageUpdate.Image]
		if !found {
			// There's no change to make in this case.
			continue
		}
		if imageUpdate.Value == api.ImageUpdateValueTypeImage {
			changes[imageUpdate.Key] = fmt.Sprintf("%s:%s", imageUpdate.Image, tag)
		} else {
			changes[imageUpdate.Key] = tag
		}
	}
	return changes
}
