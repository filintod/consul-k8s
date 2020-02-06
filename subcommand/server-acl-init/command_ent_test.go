// +build enterprise

package serveraclinit

import (
	"testing"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/mitchellh/cli"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

// Test the auth method and acl binding rule created when namespaces are enabled
// and there's a single consul destination namespace.
func TestRun_ConnectInject_SingleDestinationNamespace(t *testing.T) {
	t.Parallel()

	consulDestNamespaces := []string{"default", "destination"}
	for _, consulDestNamespace := range consulDestNamespaces {
		t.Run(consulDestNamespace, func(tt *testing.T) {
			k8s, testAgent := completeEnterpriseSetup(tt, resourcePrefix, ns)
			defer testAgent.Stop()
			setUpK8sServiceAccount(tt, k8s)
			require := require.New(tt)

			ui := cli.NewMockUi()
			cmd := Command{
				UI:        ui,
				clientset: k8s,
			}
			cmd.init()
			args := []string{
				"-server-label-selector=component=server,app=consul,release=" + releaseName,
				"-resource-prefix=" + resourcePrefix,
				"-k8s-namespace=" + ns,
				"-expected-replicas=1",
				"-create-inject-auth-method",
				"-enable-namespaces",
				"-consul-inject-destination-namespace", consulDestNamespace,
				"-acl-binding-rule-selector=serviceaccount.name!=default",
			}

			responseCode := cmd.Run(args)
			require.Equal(0, responseCode, ui.ErrorWriter.String())

			bootToken := getBootToken(t, k8s, resourcePrefix, ns)
			consul, err := api.NewClient(&api.Config{
				Address: testAgent.HTTPAddr,
				Token:   bootToken,
			})
			require.NoError(err)

			// Ensure there's only one auth method.
			namespaceQuery := &api.QueryOptions{
				Namespace: consulDestNamespace,
			}
			methods, _, err := consul.ACL().AuthMethodList(namespaceQuery)
			require.NoError(err)
			require.Len(methods, 1)

			// Check the ACL auth method is created in the expected namespace.
			authMethodName := releaseName + "-consul-k8s-auth-method"
			actMethod, _, err := consul.ACL().AuthMethodRead(authMethodName, namespaceQuery)
			require.NoError(err)
			require.NotNil(actMethod)
			require.Equal("kubernetes", actMethod.Type)
			require.Equal("Kubernetes AuthMethod", actMethod.Description)
			require.NotContains(actMethod.Config, "MapNamespaces")
			require.NotContains(actMethod.Config, "ConsulNamespacePrefix")

			// Check the binding rule is as expected.
			rules, _, err := consul.ACL().BindingRuleList(authMethodName, namespaceQuery)
			require.NoError(err)
			require.Len(rules, 1)
			actRule, _, err := consul.ACL().BindingRuleRead(rules[0].ID, namespaceQuery)
			require.NoError(err)
			require.NotNil(actRule)
			require.Equal("Kubernetes binding rule", actRule.Description)
			require.Equal(api.BindingRuleBindTypeService, actRule.BindType)
			require.Equal("${serviceaccount.name}", actRule.BindName)
			require.Equal("serviceaccount.name!=default", actRule.Selector)
		})
	}
}

// Test the auth method and acl binding rule created when namespaces are enabled
// and we're mirroring namespaces.
func TestRun_ConnectInject_NamespaceMirroring(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		MirroringPrefix string
		ExtraFlags      []string
	}{
		"no prefix": {
			MirroringPrefix: "",
			ExtraFlags:      nil,
		},
		"with prefix": {
			MirroringPrefix: "prefix-",
			ExtraFlags:      nil,
		},
		"with destination namespace flag": {
			MirroringPrefix: "",
			// Mirroring takes precedence over this flag so it should have no
			// effect.
			ExtraFlags: []string{"-consul-inject-destination-namespace=dest"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(tt *testing.T) {
			k8s, testAgent := completeEnterpriseSetup(t, resourcePrefix, ns)
			defer testAgent.Stop()
			setUpK8sServiceAccount(tt, k8s)
			require := require.New(tt)

			ui := cli.NewMockUi()
			cmd := Command{
				UI:        ui,
				clientset: k8s,
			}
			cmd.init()
			args := []string{
				"-server-label-selector=component=server,app=consul,release=" + releaseName,
				"-resource-prefix=" + resourcePrefix,
				"-k8s-namespace=" + ns,
				"-expected-replicas=1",
				"-create-inject-auth-method",
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix", c.MirroringPrefix,
				"-acl-binding-rule-selector=serviceaccount.name!=default",
			}
			args = append(args, c.ExtraFlags...)
			responseCode := cmd.Run(args)
			require.Equal(0, responseCode, ui.ErrorWriter.String())

			bootToken := getBootToken(t, k8s, resourcePrefix, ns)
			consul, err := api.NewClient(&api.Config{
				Address: testAgent.HTTPAddr,
				Token:   bootToken,
			})
			require.NoError(err)

			// Check the ACL auth method is as expected.
			authMethodName := releaseName + "-consul-k8s-auth-method"
			method, _, err := consul.ACL().AuthMethodRead(authMethodName, nil)
			require.NoError(err)
			require.NotNil(method, authMethodName+" not found")
			require.Equal("kubernetes", method.Type)
			require.Equal("Kubernetes AuthMethod", method.Description)
			require.Contains(method.Config, "MapNamespaces")
			require.Contains(method.Config, "ConsulNamespacePrefix")
			require.Equal(true, method.Config["MapNamespaces"])
			require.Equal(c.MirroringPrefix, method.Config["ConsulNamespacePrefix"])

			// Check the binding rule is as expected.
			rules, _, err := consul.ACL().BindingRuleList(authMethodName, nil)
			require.NoError(err)
			require.Len(rules, 1)
			actRule, _, err := consul.ACL().BindingRuleRead(rules[0].ID, nil)
			require.NoError(err)
			require.NotNil(actRule)
			require.Equal("Kubernetes binding rule", actRule.Description)
			require.Equal(api.BindingRuleBindTypeService, actRule.BindType)
			require.Equal("${serviceaccount.name}", actRule.BindName)
			require.Equal("serviceaccount.name!=default", actRule.Selector)
		})
	}
}

// Test that ACL policies get updated if namespaces config changes.
func TestRun_ACLPolicyUpdates(t *testing.T) {
	t.Parallel()

	k8sNamespaceFlags := []string{"default", "other"}
	for _, k8sNamespaceFlag := range k8sNamespaceFlags {
		t.Run(k8sNamespaceFlag, func(t *testing.T) {
			k8s, testAgent := completeEnterpriseSetup(t, resourcePrefix, k8sNamespaceFlag)
			defer testAgent.Stop()
			require := require.New(t)

			ui := cli.NewMockUi()
			firstRunArgs := []string{
				"-server-label-selector=component=server,app=consul,release=" + releaseName,
				"-resource-prefix=" + resourcePrefix,
				"-k8s-namespace", k8sNamespaceFlag,
				"-create-client-token",
				"-allow-dns",
				"-create-mesh-gateway-token",
				"-create-sync-token",
				"-create-inject-namespace-token",
				"-create-snapshot-agent-token",
				"-create-enterprise-license-token",
				"-expected-replicas=1",
			}
			// Our second run, we're going to update from namespaces disabled to
			// namespaces enabled with a single destination ns.
			secondRunArgs := append(firstRunArgs,
				"-enable-namespaces",
				"-consul-sync-destination-namespace=sync",
				"-consul-inject-destination-namespace=dest")

			// Run the command first to populate the policies.
			cmd := Command{
				UI:        ui,
				clientset: k8s,
			}
			responseCode := cmd.Run(firstRunArgs)
			require.Equal(0, responseCode, ui.ErrorWriter.String())

			bootToken := getBootToken(t, k8s, resourcePrefix, k8sNamespaceFlag)
			consul, err := api.NewClient(&api.Config{
				Address: testAgent.HTTPAddr,
				Token:   bootToken,
			})
			require.NoError(err)

			// Check that the expected policies were created.
			expectedPolicies := []string{
				"dns-policy",
				"client-token",
				"catalog-sync-token",
				"connect-inject-token",
				"mesh-gateway-token",
				"client-snapshot-agent-token",
				"enterprise-license-token",
			}
			policies, _, err := consul.ACL().PolicyList(nil)
			require.NoError(err)

			// Collect the actual policies into a map to make it easier to assert
			// on their existence and contents.
			actualPolicies := make(map[string]string)
			for _, p := range policies {
				policy, _, err := consul.ACL().PolicyRead(p.ID, nil)
				require.NoError(err)
				actualPolicies[p.Name] = policy.Rules
			}
			for _, expected := range expectedPolicies {
				actRules, ok := actualPolicies[expected]
				require.True(ok, "Did not find policy %s", expected)
				// We assert that the policy doesn't have any namespace config
				// in it because later that's what we're using to test that it
				// got updated.
				require.NotContains(actRules, "namespace")
			}

			// Re-run the command with namespace flags. The policies should be updated.
			// NOTE: We're redefining the command so that the old flag values are
			// reset.
			cmd = Command{
				UI:        ui,
				clientset: k8s,
			}
			responseCode = cmd.Run(secondRunArgs)
			require.Equal(0, responseCode, ui.ErrorWriter.String())

			// Check that the policies have all been updated.
			policies, _, err = consul.ACL().PolicyList(nil)
			require.NoError(err)

			// Collect the actual policies into a map to make it easier to assert
			// on their existence and contents.
			actualPolicies = make(map[string]string)
			for _, p := range policies {
				policy, _, err := consul.ACL().PolicyRead(p.ID, nil)
				require.NoError(err)
				actualPolicies[p.Name] = policy.Rules
			}
			for _, expected := range expectedPolicies {
				actRules, ok := actualPolicies[expected]
				require.True(ok, "Did not find policy %s", expected)

				switch expected {
				case "connect-inject-token":
					// The connect inject token doesn't have namespace config,
					// but does change to operator:write from an empty string.
					require.Contains(actRules, "operator = \"write\"")
				case "client-snapshot-agent-token", "enterprise-license-token":
					// The snapshot agent and enterprise license tokens shouldn't change.
					require.NotContains(actRules, "namespace")
				default:
					// Assert that the policies have the word namespace in them. This
					// tests that they were updated. The actual contents are tested
					// in rules_test.go.
					require.Contains(actRules, "namespace")
				}
			}
		})
	}
}

// Test that re-running the commands results in auth method and binding rules
// being updated.
func TestRun_ConnectInject_Updates(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// Args for first run of command.
		FirstRunArgs []string
		// Args for second run of command.
		SecondRunArgs []string
		// Expected namespace for the auth method.
		AuthMethodExpectedNS string
		// If true, we expect MapNamespaces to be set on the auth method
		// config.
		AuthMethodExpectMapNamespacesConfig bool
		// If AuthMethodExpectMapNamespacesConfig is true, we will assert
		// that the ConsulNamespacePrefix field on the auth method config
		// is set to this.
		AuthMethodExpectedNamespacePrefixConfig string
		// Expected namespace for the binding rule.
		BindingRuleExpectedNS string
	}{
		"no ns => mirroring ns, no prefix": {
			FirstRunArgs: nil,
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "",
			BindingRuleExpectedNS:                   "default",
		},
		"no ns => mirroring ns, prefix": {
			FirstRunArgs: nil,
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "prefix-",
			BindingRuleExpectedNS:                   "default",
		},
		"no ns => single dest ns": {
			FirstRunArgs: nil,
			SecondRunArgs: []string{
				"-create-inject-auth-method",
				"-enable-namespaces",
				"-consul-inject-destination-namespace=dest",
			},
			AuthMethodExpectedNS:                    "dest",
			AuthMethodExpectMapNamespacesConfig:     false,
			AuthMethodExpectedNamespacePrefixConfig: "",
			BindingRuleExpectedNS:                   "dest",
		},
		"mirroring ns => single dest ns": {
			FirstRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-consul-inject-destination-namespace=dest",
			},
			AuthMethodExpectedNS:                    "dest",
			AuthMethodExpectMapNamespacesConfig:     false,
			AuthMethodExpectedNamespacePrefixConfig: "",
			BindingRuleExpectedNS:                   "dest",
		},
		"single dest ns => mirroring ns": {
			FirstRunArgs: []string{
				"-enable-namespaces",
				"-consul-inject-destination-namespace=dest",
			},
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "prefix-",
			BindingRuleExpectedNS:                   "default",
		},
		"mirroring ns (no prefix) => mirroring ns (no prefix)": {
			FirstRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=",
			},
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "",
			BindingRuleExpectedNS:                   "default",
		},
		"mirroring ns => mirroring ns (same prefix)": {
			FirstRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "prefix-",
			BindingRuleExpectedNS:                   "default",
		},
		"mirroring ns (no prefix) => mirroring ns (prefix)": {
			FirstRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=",
			},
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "prefix-",
			BindingRuleExpectedNS:                   "default",
		},
		"mirroring ns (prefix) => mirroring ns (no prefix)": {
			FirstRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=prefix-",
			},
			SecondRunArgs: []string{
				"-enable-namespaces",
				"-enable-inject-k8s-namespace-mirroring",
				"-inject-k8s-namespace-mirroring-prefix=",
			},
			AuthMethodExpectedNS:                    "default",
			AuthMethodExpectMapNamespacesConfig:     true,
			AuthMethodExpectedNamespacePrefixConfig: "",
			BindingRuleExpectedNS:                   "default",
		},
	}

	for name, c := range cases {
		t.Run(name, func(tt *testing.T) {
			require := require.New(tt)
			k8s, testAgent := completeEnterpriseSetup(tt, resourcePrefix, ns)
			defer testAgent.Stop()
			setUpK8sServiceAccount(tt, k8s)

			ui := cli.NewMockUi()
			defaultArgs := []string{
				"-server-label-selector=component=server,app=consul,release=" + releaseName,
				"-resource-prefix=" + resourcePrefix,
				"-k8s-namespace=" + ns,
				"-expected-replicas=1",
				"-create-inject-auth-method",
			}

			// First run. NOTE: we don't assert anything here since we've
			// tested these results in other tests. What we care about here
			// is the result after the second run.
			cmd := Command{
				UI:        ui,
				clientset: k8s,
			}
			responseCode := cmd.Run(append(defaultArgs, c.FirstRunArgs...))
			require.Equal(0, responseCode, ui.ErrorWriter.String())

			// Second run.
			// NOTE: We're redefining the command so that the old flag values are
			// reset.
			cmd = Command{
				UI:        ui,
				clientset: k8s,
			}
			responseCode = cmd.Run(append(defaultArgs, c.SecondRunArgs...))
			require.Equal(0, responseCode, ui.ErrorWriter.String())

			// Now check that everything is as expected.
			bootToken := getBootToken(t, k8s, resourcePrefix, ns)
			consul, err := api.NewClient(&api.Config{
				Address: testAgent.HTTPAddr,
				Token:   bootToken,
			})
			require.NoError(err)

			// Check the ACL auth method is as expected.
			authMethodName := releaseName + "-consul-k8s-auth-method"
			method, _, err := consul.ACL().AuthMethodRead(authMethodName, &api.QueryOptions{
				Namespace: c.AuthMethodExpectedNS,
			})
			require.NoError(err)
			require.NotNil(method, authMethodName+" not found")
			if c.AuthMethodExpectMapNamespacesConfig {
				require.Contains(method.Config, "MapNamespaces")
				require.Contains(method.Config, "ConsulNamespacePrefix")
				require.Equal(true, method.Config["MapNamespaces"])
				require.Equal(c.AuthMethodExpectedNamespacePrefixConfig, method.Config["ConsulNamespacePrefix"])
			} else {
				require.NotContains(method.Config, "MapNamespaces")
				require.NotContains(method.Config, "ConsulNamespacePrefix")
			}

			// Check the binding rule is as expected.
			rules, _, err := consul.ACL().BindingRuleList(authMethodName, &api.QueryOptions{
				Namespace: c.BindingRuleExpectedNS,
			})
			require.NoError(err)
			require.Len(rules, 1)
		})
	}
}

// Set up test consul agent and kubernetes cluster.
func completeEnterpriseSetup(t *testing.T, prefix string, k8sNamespace string) (*fake.Clientset, *testutil.TestServer) {
	k8s := fake.NewSimpleClientset()

	svr, err := testutil.NewTestServerConfigT(t, func(c *testutil.TestServerConfig) {
		c.ACL.Enabled = true
	})
	require.NoError(t, err)

	createTestK8SResources(t, k8s, svr.HTTPAddr, prefix, "http", k8sNamespace)

	return k8s, svr
}
