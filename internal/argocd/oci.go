package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ociScheme is the scheme of an OCI reference, which an Application may name a chart with
// and the oci-tags endpoint of ArgoCD expects.
const ociScheme = "oci://"

// listOCITags returns the tags ArgoCD reports for an OCI artifact, e.g.
// "ghcr.io/myorg/charts/nginx". ArgoCD reaches the registry with the credentials it stores
// for the repository, so Argazer never needs them itself.
//
// project is the ArgoCD project of the asking application, needed for repositories whose
// credentials are scoped to a project.
func (r *restClient) listOCITags(ctx context.Context, artifact, project string) ([]string, error) {
	var found refs
	if err := r.getJSON(ctx, r.repositoryEndpoint(ociRepositoryURL(artifact), "oci-tags", project), &found); err != nil {
		var statusErr *responseStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
			// The endpoint itself may be the thing that is missing: it was added in ArgoCD
			// 3.1, which is easy to run into and hard to guess from a bare 404.
			return nil, fmt.Errorf("%w; OCI tags require ArgoCD 3.1 or newer and the repository to be registered in ArgoCD", err)
		}

		return nil, err
	}

	// Only the tags are of interest: ArgoCD answers OCI tags with the same message it uses
	// for Git refs, whose branches stay empty here.
	return found.Tags, nil
}

// ociArtifact builds the reference of the OCI artifact holding a chart, which is what
// ArgoCD lists the tags of.
//
// The repository URL is passed on exactly as the Application spells it, because that same
// string is what ArgoCD looks its credentials up by, and it matches a registered
// repository only as a whole. Dropping the oci:// scheme, for one, would turn a repository
// registered as "oci://ghcr.io/myorg/nginx" into an unknown one, and ArgoCD would fall
// back to an anonymous request the registry rejects.
//
// A source with the scheme is an OCI source to ArgoCD: its URL already names the artifact
// and a chart name, should the Application carry one, is not part of the reference. A
// source without it is a Helm source, which names the registry path and the chart
// separately the way Helm does, so chart "nginx" of "ghcr.io/myorg/charts" lives in
// "ghcr.io/myorg/charts/nginx".
func ociArtifact(repoURL, chartName string) string {
	if strings.HasPrefix(strings.ToLower(repoURL), ociScheme) {
		return strings.TrimSuffix(repoURL, "/")
	}

	artifact := strings.Trim(repoURL, "/")
	chartName = strings.Trim(chartName, "/")
	if chartName == "" {
		return artifact
	}

	return artifact + "/" + chartName
}

// ociRepositoryURL is the repository URL the oci-tags endpoint takes for an artifact.
//
// The endpoint reads the registry out of the URL it is handed and needs the oci:// scheme to
// find it: an artifact named as the bare registry path an Application may spell it as is
// answered with "invalid registry" instead of its tags. The scheme is therefore added to an
// artifact that goes without one.
//
// repositoryEndpoint escapes the result as a single path segment, the slashes and the colon
// of the scheme included, which is what keeps it one path parameter of the request.
func ociRepositoryURL(artifact string) string {
	if strings.HasPrefix(strings.ToLower(artifact), ociScheme) {
		return artifact
	}

	return ociScheme + artifact
}
