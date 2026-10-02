// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Where a pod finds its own service account.
const (
	saDir      = "/var/run/secrets/kubernetes.io/serviceaccount"
	saTokenRel = "token"
	saCARel    = "ca.crt"
)

// kubeResource says where a resource lives in the API. Only listed resources
// can be read, which also keeps secrets out of reach: there is no entry for
// them and the pack validator refuses the name outright.
type kubeResource struct {
	prefix     string // "/api/v1" or "/apis/<group>/<version>"
	name       string
	namespaced bool
}

var kubeResources = func() map[string]kubeResource {
	m := map[string]kubeResource{}
	add := func(prefix string, namespaced bool, names ...string) {
		for _, n := range names {
			m[n] = kubeResource{prefix: prefix, name: n, namespaced: namespaced}
		}
	}
	add("/api/v1", true, "pods", "services", "endpoints", "configmaps", "persistentvolumeclaims", "serviceaccounts", "events")
	add("/api/v1", false, "nodes", "namespaces", "persistentvolumes")
	add("/apis/apps/v1", true, "deployments", "statefulsets", "daemonsets", "replicasets")
	add("/apis/batch/v1", true, "jobs", "cronjobs")
	add("/apis/networking.k8s.io/v1", true, "ingresses", "networkpolicies")
	return m
}()

// KubeResourceNames lists what the in-cluster client can read, for the
// chart's RBAC rules and for error messages.
func KubeResourceNames() []string {
	out := make([]string, 0, len(kubeResources))
	for n := range kubeResources {
		out = append(out, n)
	}
	return out
}

// InCluster reports whether this process runs in a pod with a service account.
func InCluster() bool {
	return inClusterAt(saDir, os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"))
}

func inClusterAt(dir, host, port string) bool {
	if host == "" || port == "" {
		return false
	}
	_, err := os.Stat(dir + "/" + saTokenRel)
	return err == nil
}

// KubeAPI returns a KubectlGet that reads the cluster the pod runs in through
// its service account, with no kubectl binary. The token is read on every
// request because projected tokens rotate.
func KubeAPI() KubectlGet {
	return kubeAPIAt(saDir, os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"), nil)
}

func kubeAPIAt(dir, host, port string, client *http.Client) KubectlGet {
	return func(ctx context.Context, q KubeQuery) (io.ReadCloser, error) {
		res, ok := kubeResources[q.Resource]
		if !ok {
			return nil, fmt.Errorf("the in-cluster client cannot read %q (readable: %s); use kubectl for anything else", q.Resource, strings.Join(sortedKeys(kubeResources), ", "))
		}
		tok, err := os.ReadFile(dir + "/" + saTokenRel)
		if err != nil {
			return nil, fmt.Errorf("service account token: %w", err)
		}
		cli := client
		if cli == nil {
			ca, err := os.ReadFile(dir + "/" + saCARel)
			if err != nil {
				return nil, fmt.Errorf("service account CA: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				return nil, errors.New("service account CA holds no certificate")
			}
			// No overall timeout: the context bounds the call, and a large
			// listing is streamed.
			cli = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
		}
		path := res.prefix
		if res.namespaced && q.Namespace != "" {
			path += "/namespaces/" + url.PathEscape(q.Namespace)
		}
		path += "/" + res.name
		u := url.URL{Scheme: "https", Host: net.JoinHostPort(host, port), Path: path}
		qs := url.Values{}
		if q.Selector != "" {
			qs.Set("labelSelector", q.Selector)
		}
		if q.FieldSelector != "" {
			qs.Set("fieldSelector", q.FieldSelector)
		}
		u.RawQuery = qs.Encode()
		return kubeGet(ctx, cli, u.String(), strings.TrimSpace(string(tok)))
	}
}

func kubeGet(ctx context.Context, cli *http.Client, target, token string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kubernetes API: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var st struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = json.Unmarshal(b, &st)
		msg := st.Message
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusForbidden {
			msg += " (the service account needs get and list on this resource; the chart grants it with kubernetes.inCluster=true)"
		}
		return nil, fmt.Errorf("kubernetes API: %s", msg)
	}
	return &capReader{rc: resp.Body}, nil
}

// capReader enforces the same size cap as the kubectl path.
type capReader struct {
	rc   io.ReadCloser
	read int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.read += int64(n)
	if c.read > maxKubeOutput {
		return n, fmt.Errorf("the listing is larger than %d MiB; narrow it with k8s_selector, k8s_field_selector or k8s_namespace", maxKubeOutput>>20)
	}
	return n, err
}

func (c *capReader) Close() error { return c.rc.Close() }

func sortedKeys(m map[string]kubeResource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// small and fixed: insertion sort keeps the file free of another import
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
