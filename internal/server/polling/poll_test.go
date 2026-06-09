package polling

import (
	"context"
	"testing"

	"github.com/flux-iac/tofu-controller/internal/config"
	bpconfig "github.com/flux-iac/tofu-controller/internal/config"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/flux-iac/tofu-controller/internal/git/provider/providerfakes"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/flux-iac/tofu-controller/api/v1alpha2"
	"github.com/flux-iac/tofu-controller/internal/git/provider"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
)

// This checks poll can be called with a little setting-up, with no
// result expected.
func Test_poll_empty(t *testing.T) {
	g := gomega.NewWithT(t)
	ns := newNamespace(t, g)

	// Create a source for the Terraform object to point to
	source := &sourcev1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "original-source",
			Namespace: ns.Name,
		},
		Spec: sourcev1.GitRepositorySpec{
			URL: "https://github.com/flux-iac/tofu-controller",
			Reference: &sourcev1.GitRepositoryRef{
				Branch: "main",
			},
		},
	}
	expectToSucceed(t, g, k8sClient.Create(t.Context(), source))

	// Create a Terraform object to be the template.
	original := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "original",
			Namespace: ns.Name,
		},
		Spec: infrav1.TerraformSpec{
			SourceRef: infrav1.CrossNamespaceSourceReference{
				Name: source.Name,
				Kind: "GitRepository",
			},
		},
	}
	expectToSucceed(t, g, k8sClient.Create(t.Context(), original))

	// This fakes a provider for the server to use.
	var prs []provider.PullRequest

	server, err := New(
		WithClusterClient(k8sClient),
	)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Now we'll run `reconcile` to step the server once, and afterwards,
	// we should be able to see what it did.
	ctx := t.Context()
	expectToSucceed(t, g, server.reconcile(ctx, original, nil, source, prs, &providerfakes.FakeProvider{}))

	// We expect it to have done nothing! So, check it didn't create
	// any more Terraform or source objects.
	var tfList infrav1.TerraformList
	expectToSucceed(t, g, k8sClient.List(t.Context(), &tfList, &client.ListOptions{
		Namespace: ns.Name,
	}))
	expectToEqual(t, g, len(tfList.Items), 1) // just the original
	expectToEqual(t, g, tfList.Items[0].Name, original.Name)

	var srcList sourcev1.GitRepositoryList
	expectToSucceed(t, g, k8sClient.List(t.Context(), &srcList, &client.ListOptions{
		Namespace: ns.Name,
	}))
	expectToEqual(t, g, len(srcList.Items), 1) // just `source`
	expectToEqual(t, g, srcList.Items[0].Name, source.Name)

	t.Cleanup(func() { expectToSucceed(t, g, k8sClient.Delete(context.Background(), ns)) })
}

// This checks that branch Terraform objects are created,
// when there are open pull requests,
// updated when the original Terraform object is updated,
// and deleted when the corresponding PRs are closed.
// The original Terraform object and source should be retained.
func Test_poll_reconcile_objects(t *testing.T) {
	g := gomega.NewWithT(t)
	ns := newNamespace(t, g)

	// Create a source for the Terraform object to point to
	source := &sourcev1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "original-source",
			Namespace: ns.Name,
			Labels: map[string]string{
				"test-label": "123",
			},
		},
		Spec: sourcev1.GitRepositorySpec{
			URL: "https://github.com/tf-controller/helloworld",
			Reference: &sourcev1.GitRepositoryRef{
				Branch: "main",
			},
		},
	}
	expectToSucceed(t, g, k8sClient.Create(t.Context(), source))

	// Create a Terraform object to be the template.
	original := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "original",
			Namespace: ns.Name,
			Labels: map[string]string{
				"test-label": "abc",
			},
		},
		Spec: infrav1.TerraformSpec{
			SourceRef: infrav1.CrossNamespaceSourceReference{
				Name: source.Name,
				Kind: "GitRepository",
			},
			WriteOutputsToSecret: &infrav1.WriteOutputsToSecretSpec{
				Name: "test-secret",
			},
			ApprovePlan: "should be cleared",
			Force:       true, // should be set false on clone.
		},
	}
	expectToSucceed(t, g, k8sClient.Create(t.Context(), original))

	// This fakes a provider for the server to use.
	repo := provider.Repository{
		Project: "fake-project",
		Org:     "fake-org",
		Name:    "fake-name",
	}
	prs := []provider.PullRequest{
		{
			Repository: repo,
			Number:     1,
			BaseBranch: "main",
			HeadBranch: "test-branch-1",
		},
		{
			Repository: repo,
			Number:     2,
			BaseBranch: "main",
			HeadBranch: "test-branch-2",
		},
		{
			Repository: repo,
			Number:     3,
			BaseBranch: "main",
			HeadBranch: "test-branch-3",
		},
	}

	server, err := New(
		WithClusterClient(k8sClient),
	)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Now we'll run `reconcile` to step the server once, and afterwards,
	// we should be able to see what it did.
	ctx := t.Context()
	expectToSucceed(t, g, server.reconcile(ctx, original, nil, source, prs, &providerfakes.FakeProvider{}))

	// We expect the branch TF objects and corresponding sources
	// to be created for each PR
	// and the original object and source to be retained.

	// Check that the Terraform objects are created with expected fields.
	var tfList infrav1.TerraformList
	expectToSucceed(t, g, k8sClient.List(t.Context(), &tfList, &client.ListOptions{
		Namespace: ns.Name,
	}))

	expectToEqual(t, g, len(tfList.Items), 4)
	for _, item := range tfList.Items {
		if item.Name == original.Name {
			// Ignore the original source object.
			continue
		}
		prID := item.Labels[bpconfig.LabelPRIDKey]
		expectToEqual(t, g, item.Name, config.PullRequestObjectName(original.Name, prID))
		expectToEqual(t, g, item.Spec.SourceRef.Name, config.SourceName(original.Name, source.Name, prID))
		expectToEqual(t, g, item.Spec.SourceRef.Namespace, ns.Name)
		expectToEqual(t, g, item.Spec.PlanOnly, true)
		expectToEqual(t, g, item.Spec.StoreReadablePlan, "human")
		expectToEqual(t, g, item.Spec.ApprovePlan, "")
		expectToEqual(t, g, item.Spec.Force, false)
		g.Expect(item.Spec.WriteOutputsToSecret).To(gomega.BeNil()) // we don't need to use the output Secret of the plan
		expectToEqual(t, g, item.Labels[bpconfig.LabelKey], bpconfig.LabelValue)
		expectToEqual(t, g, item.Labels["test-label"], "abc")
		expectToEqual(t, g, item.Spec.BackendConfig.SecretSuffix, original.Name)
		expectToEqual(t, g, item.Spec.BackendConfig.InClusterConfig, true)
	}

	// Check that the Source objects are created with all expected fields.
	var srcList sourcev1.GitRepositoryList
	expectToSucceed(t, g, k8sClient.List(t.Context(), &srcList, &client.ListOptions{
		Namespace: ns.Name,
	}))

	expectToEqual(t, g, len(srcList.Items), 4)
	for _, item := range srcList.Items {
		if item.Name == source.Name {
			// Ignore the original source object.
			continue
		}
		prID := item.Labels[bpconfig.LabelPRIDKey]
		expectToEqual(t, g, item.Name, config.SourceName(original.Name, source.Name, prID))
		expectToEqual(t, g, item.Spec.Reference.Branch, "test-branch-"+prID)
		expectToEqual(t, g, item.Labels[bpconfig.LabelKey], bpconfig.LabelValue)
		expectToEqual(t, g, item.Labels[bpconfig.LabelPRIDKey], prID)
		expectToEqual(t, g, item.Labels["test-label"], "123")
	}

	// Check that branch Terraform objects are updated
	// after the original Terraform object is updated.
	secretName := "new-test-secret"
	original.Labels["test-label"] = "xyz"
	original.Spec.WriteOutputsToSecret.Name = secretName

	expectToSucceed(t, g, k8sClient.Update(t.Context(), original))
	expectToSucceed(t, g, server.reconcile(ctx, original, nil, source, prs, &providerfakes.FakeProvider{}))

	tfList.Items = nil

	expectToSucceed(t, g, k8sClient.List(t.Context(), &tfList, &client.ListOptions{
		Namespace:     ns.Name,
		LabelSelector: labels.Set{bpconfig.LabelKey: bpconfig.LabelValue}.AsSelector(),
	}))

	for _, item := range tfList.Items {
		expectToEqual(t, g, item.Labels["test-label"], "xyz")
		g.Expect(item.Spec.WriteOutputsToSecret).To(gomega.BeNil())
	}

	// Check that corresponding Terraform objects and Sources are deleted
	// after PRs are deleted
	// and the original Terraform object and source are retained.
	prs = prs[2:]

	expectToSucceed(t, g, server.reconcile(ctx, original, nil, source, prs, &providerfakes.FakeProvider{}))

	tfList.Items = nil

	expectToSucceed(t, g, k8sClient.List(t.Context(), &tfList, &client.ListOptions{
		Namespace: ns.Name,
	}))

	expectToEqual(t, g, len(tfList.Items), 2)
	expectToEqual(t, g, tfList.Items[0].Name, original.Name)
	expectToEqual(t, g, tfList.Items[1].Name, original.Name+"-pr-3")

	srcList.Items = nil

	expectToSucceed(t, g, k8sClient.List(t.Context(), &srcList, &client.ListOptions{
		Namespace: ns.Name,
	}))

	expectToEqual(t, g, len(srcList.Items), 2)
	for _, item := range srcList.Items {
		if item.Name == source.Name {
			continue
		}
		// Only one item left and it should be PR#3.
		expectToEqual(t, g, item.Name, config.SourceName(original.Name, source.Name, "3"))
	}

	t.Cleanup(func() { expectToSucceed(t, g, k8sClient.Delete(context.Background(), ns)) })
}

// If there are no Terraform changes in a Pull Request, and
// `.spec.BranchPlanner.EnablePathScope` is true, we expect no new resources are
// created for that Pull Request.
func Test_poll_noPathChanges(t *testing.T) {
	g := gomega.NewWithT(t)
	ns := newNamespace(t, g)

	// Create a source for the Terraform object to point to
	source := &sourcev1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "original-source",
			Namespace: ns.Name,
		},
		Spec: sourcev1.GitRepositorySpec{
			URL: "https://github.com/flux-iac/tofu-controller",
			Reference: &sourcev1.GitRepositoryRef{
				Branch: "main",
			},
		},
	}
	expectToSucceed(t, g, k8sClient.Create(t.Context(), source))

	// Create a Terraform object to be the template.
	original := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "original",
			Namespace: ns.Name,
		},
		Spec: infrav1.TerraformSpec{
			Path: "./infra/",
			SourceRef: infrav1.CrossNamespaceSourceReference{
				Name: source.Name,
				Kind: "GitRepository",
			},
			BranchPlanner: &infrav1.BranchPlanner{
				EnablePathScope: true,
			},
			BackendConfig: &infrav1.BackendConfigSpec{
				SecretSuffix: "special-value",
			},
		},
	}
	expectToSucceed(t, g, k8sClient.Create(t.Context(), original))

	repo := provider.Repository{
		Project: "fake-project",
		Org:     "fake-org",
		Name:    "fake-name",
	}
	prs := []provider.PullRequest{
		{
			Repository: repo,
			Number:     1,
			BaseBranch: "main",
			HeadBranch: "test-branch-1",
		},
	}
	prChanges := []provider.Change{
		{
			Path:      "cmd/project/main.go",
			Additions: 2,
			Added:     true,
		},
	}

	gitProvider := &providerfakes.FakeProvider{}
	gitProvider.ListPullRequestChangesReturns(prChanges, nil)

	server, err := New(
		WithClusterClient(k8sClient),
	)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Now we'll run `reconcile` to step the server once, and afterwards,
	// we should be able to see what it did.
	ctx := t.Context()
	expectToSucceed(t, g, server.reconcile(ctx, original, nil, source, prs, gitProvider))

	// We expect it to have done nothing! So, check it didn't create
	// any more Terraform or source objects.
	var tfList infrav1.TerraformList
	expectToSucceed(t, g, k8sClient.List(t.Context(), &tfList, &client.ListOptions{
		Namespace: ns.Name,
	}))
	expectToEqual(t, g, len(tfList.Items), 1, "terraform list") // just the original
	expectToEqual(t, g, tfList.Items[0].Name, original.Name)
	expectToEqual(t, g, tfList.Items[0].Spec.BackendConfig, original.Spec.BackendConfig)

	var srcList sourcev1.GitRepositoryList
	expectToSucceed(t, g, k8sClient.List(t.Context(), &srcList, &client.ListOptions{
		Namespace: ns.Name,
	}))
	expectToEqual(t, g, len(srcList.Items), 1, "source list") // just `source`
	expectToEqual(t, g, srcList.Items[0].Name, source.Name)

	t.Cleanup(func() { expectToSucceed(t, g, k8sClient.Delete(context.Background(), ns)) })
}

// Test_filterPullRequestsByPath exercises the path-scope filter directly with
// a fake git provider, so we can fan out cases over the three OR'd sources
// (spec.path prefix, ConfigMap-level additionalPaths, CR-level additionalPaths)
// without spinning up full reconcile() machinery.
func Test_filterPullRequestsByPath(t *testing.T) {
	cases := []struct {
		name            string
		enablePathScope bool
		specPath        string
		configMapGlobs  []string
		crGlobs         []string
		changedFiles    []string
		wantPRIncluded  bool
	}{
		{
			name:            "path-scope disabled bypasses filter",
			enablePathScope: false,
			specPath:        "infra/terraform/environments/global",
			changedFiles:    []string{"README.md"},
			wantPRIncluded:  true,
		},
		{
			name:            "spec.path prefix matches (unchanged behavior)",
			enablePathScope: true,
			specPath:        "./infra/terraform/environments/global",
			changedFiles:    []string{"infra/terraform/environments/global/main.tf"},
			wantPRIncluded:  true,
		},
		{
			name:            "spec.path prefix doesn't match",
			enablePathScope: true,
			specPath:        "./infra/terraform/environments/global",
			changedFiles:    []string{"README.md"},
			wantPRIncluded:  false,
		},
		{
			name:            "ConfigMap glob matches a literal path",
			enablePathScope: true,
			specPath:        "infra/terraform/environments/global",
			configMapGlobs:  []string{"infra/tenant-infrastructure-configs.yaml"},
			changedFiles:    []string{"infra/tenant-infrastructure-configs.yaml"},
			wantPRIncluded:  true,
		},
		{
			name:            "ConfigMap doublestar glob matches recursively",
			enablePathScope: true,
			specPath:        "infra/terraform/environments/global",
			configMapGlobs:  []string{"infra/terraform/modules/**"},
			changedFiles:    []string{"infra/terraform/modules/networking/main.tf"},
			wantPRIncluded:  true,
		},
		{
			name:            "CR-level glob matches (independent of ConfigMap)",
			enablePathScope: true,
			specPath:        "infra/terraform/environments/global",
			crGlobs:         []string{"infra/global/*.yaml"},
			changedFiles:    []string{"infra/global/tenants.yaml"},
			wantPRIncluded:  true,
		},
		{
			name:            "empty spec.path + ConfigMap glob still filters",
			enablePathScope: true,
			specPath:        "",
			configMapGlobs:  []string{"infra/**"},
			changedFiles:    []string{"docs/README.md"},
			wantPRIncluded:  false,
		},
		{
			name:            "empty spec.path + matching ConfigMap glob",
			enablePathScope: true,
			specPath:        "",
			configMapGlobs:  []string{"infra/**"},
			changedFiles:    []string{"infra/anything.txt"},
			wantPRIncluded:  true,
		},
		{
			name:            "invalid glob logged, valid glob still matches",
			enablePathScope: true,
			specPath:        "infra/terraform/environments/global",
			configMapGlobs:  []string{"[bad-bracket", "infra/terraform/modules/**"},
			changedFiles:    []string{"infra/terraform/modules/networking/main.tf"},
			wantPRIncluded:  true,
		},
		{
			name:            "no matches across any source",
			enablePathScope: true,
			specPath:        "infra/terraform/environments/global",
			configMapGlobs:  []string{"infra/terraform/modules/**"},
			crGlobs:         []string{"docs/*.md"},
			changedFiles:    []string{"unrelated/file.txt"},
			wantPRIncluded:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)

			tf := &infrav1.Terraform{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tf-under-test",
					Namespace: "test-ns",
				},
				Spec: infrav1.TerraformSpec{
					Path: tc.specPath,
					BranchPlanner: &infrav1.BranchPlanner{
						EnablePathScope: tc.enablePathScope,
						AdditionalPaths: tc.crGlobs,
					},
				},
			}

			changes := make([]provider.Change, 0, len(tc.changedFiles))
			for _, p := range tc.changedFiles {
				changes = append(changes, provider.Change{Path: p, Added: true})
			}
			fakeProvider := &providerfakes.FakeProvider{}
			fakeProvider.ListPullRequestChangesReturns(changes, nil)

			prs := []provider.PullRequest{{Number: 42, BaseBranch: "main", HeadBranch: "feature"}}

			server, err := New(WithClusterClient(k8sClient))
			g.Expect(err).NotTo(gomega.HaveOccurred())

			out := server.filterPullRequestsByPath(t.Context(), tf, tc.configMapGlobs, fakeProvider, prs)
			if tc.wantPRIncluded {
				g.Expect(out).To(gomega.HaveLen(1), "PR should pass the filter")
			} else {
				g.Expect(out).To(gomega.HaveLen(0), "PR should be filtered out")
			}
		})
	}
}
