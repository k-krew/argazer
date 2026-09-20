package argocd

// The types here are the shapes Argazer reads out of the answers of the ArgoCD REST API.
// They carry only the fields that are actually used: ArgoCD answers with a great deal
// more, and parsing just this much is what keeps the ArgoCD SDK, along with the gRPC,
// Kubernetes and Helm libraries it pulls in, out of the binary.

// SourceTypeHelm is what ArgoCD records for a source it renders with Helm. The other
// values it uses are Kustomize, Directory and Plugin, none of which hold a chart.
const SourceTypeHelm = "Helm"

// Application is an ArgoCD Application as far as Argazer is concerned.
type Application struct {
	Metadata ApplicationMetadata `json:"metadata"`
	Spec     ApplicationSpec     `json:"spec"`
	Status   ApplicationStatus   `json:"status"`
}

// ApplicationMetadata is the Kubernetes object metadata of an Application.
type ApplicationMetadata struct {
	Name string `json:"name"`
}

// ApplicationSpec is what an Application asks ArgoCD to deploy.
type ApplicationSpec struct {
	// Project is the ArgoCD project of the Application, which is what the credentials of
	// a project-scoped repository are resolved by.
	Project string `json:"project"`

	// Source is set on a single-source Application, Sources on a multi-source one. An
	// Application carries one or the other, never both.
	Source  *ApplicationSource  `json:"source,omitempty"`
	Sources []ApplicationSource `json:"sources,omitempty"`
}

// ApplicationStatus is what ArgoCD worked out about an Application while processing it.
type ApplicationStatus struct {
	// SourceType is the tool ArgoCD renders a single-source Application with, SourceTypes
	// holds one entry per source of a multi-source one, in the order the sources are
	// listed. Both stay empty until ArgoCD has processed the Application for the first
	// time, so neither can be relied upon to be there.
	SourceType  string   `json:"sourceType,omitempty"`
	SourceTypes []string `json:"sourceTypes,omitempty"`
}

// ApplicationSource is one place an Application takes manifests from.
type ApplicationSource struct {
	// Name is how a source of a multi-source Application is referred to, and is what
	// --source-name picks a source by.
	Name string `json:"name,omitempty"`

	// RepoURL is the repository as ArgoCD knows it, which is also the string its
	// credentials are registered under.
	RepoURL string `json:"repoURL,omitempty"`

	// Chart is the chart of a Helm repository or OCI registry. A source kept in Git has
	// none and names a Path instead.
	Chart string `json:"chart,omitempty"`

	// Path is the directory of a Git repository the manifests, or the chart, live in.
	Path string `json:"path,omitempty"`

	// Ref is the name the other sources of a multi-source Application point at this one
	// by, as in `$values/env/prod.yaml`. A source that carries a Ref and no Chart holds
	// nothing but values files.
	Ref string `json:"ref,omitempty"`

	// TargetRevision is the version, range, tag or branch the Application asks for.
	TargetRevision string `json:"targetRevision,omitempty"`

	// Helm are the Helm options of the source. Argazer only reads whether they are there
	// at all, which is what tells a Helm chart in a Git repository from plain manifests.
	Helm *ApplicationSourceHelm `json:"helm,omitempty"`
}

// ApplicationSourceHelm holds the Helm options of a source, none of which Argazer reads.
type ApplicationSourceHelm struct{}

// applicationList is the answer of GET /api/v1/applications.
type applicationList struct {
	Items []Application `json:"items"`
}

// helmChartsResponse is the answer of GET /api/v1/repositories/{repo}/helmcharts.
type helmChartsResponse struct {
	Items []helmChart `json:"items"`
}

// helmChart is one chart of a Helm repository, with every version of it ArgoCD found in
// the repository index.
type helmChart struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
}

// refs are the references of a repository. ArgoCD answers both the refs of a Git
// repository and the tags of an OCI artifact with this message; the branches stay empty
// for an artifact.
type refs struct {
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
}

// sessionRequest asks POST /api/v1/session for a token.
type sessionRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionResponse is the answer of POST /api/v1/session.
type sessionResponse struct {
	Token string `json:"token"`
}
