// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package executor

import (
	"bytes"
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

	"gopkg.in/yaml.v3"
)

const (
	saDir     = "/var/run/secrets/kubernetes.io/serviceaccount"
	gryviaAPI = "/apis/gryvia.io/v1alpha1"
	// fieldManager names Zyntra as the owner of the fields it applies.
	fieldManager = "zyntra"
)

// kubeKinds are the only objects the in-cluster runner writes: what the
// gravia.* templates render. Anything else is refused.
var kubeKinds = map[string]struct {
	resource   string
	namespaced bool
}{
	"GryviaPriority":         {"gryviapriorities", false},
	"GryviaGPUSharingPolicy": {"gryviagpusharingpolicies", false},
	"GryviaAIJob":            {"gryviaaijobs", true},
}

var kubeResources = map[string]string{
	"gryviapriorities.gryvia.io":         "GryviaPriority",
	"gryviagpusharingpolicies.gryvia.io": "GryviaGPUSharingPolicy",
	"gryviaaijobs.gryvia.io":             "GryviaAIJob",
}

// KubeAPI runs the kubectl commands the gravia.* templates render against the
// cluster the pod runs in, through its service account, with no kubectl
// binary: server-side apply for "apply -f -", a merge patch for "patch", and
// a delete of a Zyntra-managed object by name. "--dry-run=server" becomes the
// API's dryRun=All. The token is read on every call because projected tokens
// rotate.
func KubeAPI() Runner {
	return kubeAPIAt(saDir, os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"), nil)
}

// InCluster reports whether this process runs in a pod with a service account.
func InCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" || os.Getenv("KUBERNETES_SERVICE_PORT") == "" {
		return false
	}
	_, err := os.Stat(saDir + "/token")
	return err == nil
}

type kubeCall struct {
	method, path, contentType string
	body                      []byte
	// managedOnly deletes only an object labelled as managed by Zyntra.
	managedOnly    bool
	ignoreNotFound bool
}

func kubeAPIAt(dir, host, port string, cli *http.Client) Runner {
	return func(ctx context.Context, args []string, stdin string) (string, error) {
		call, dryRun, err := parseKubectl(args, stdin)
		if err != nil {
			return "", err
		}
		tok, err := os.ReadFile(dir + "/token")
		if err != nil {
			return "", fmt.Errorf("service account token: %w", err)
		}
		if cli == nil {
			if cli, err = saClient(dir); err != nil {
				return "", err
			}
		}
		k := &kube{cli: cli, base: "https://" + net.JoinHostPort(host, port), token: strings.TrimSpace(string(tok))}
		return k.run(ctx, call, dryRun)
	}
}

func saClient(dir string) (*http.Client, error) {
	ca, err := os.ReadFile(dir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("service account CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("service account CA holds no certificate")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
}

func objectPath(kind, namespace, name string) (string, error) {
	k, ok := kubeKinds[kind]
	if !ok {
		return "", fmt.Errorf("the in-cluster runner cannot write %s (only the gravia.* templates' kinds)", kind)
	}
	p := gryviaAPI
	if k.namespaced {
		if namespace == "" {
			return "", fmt.Errorf("%s needs a namespace", kind)
		}
		p += "/namespaces/" + url.PathEscape(namespace)
	}
	return p + "/" + k.resource + "/" + url.PathEscape(name), nil
}

// parseKubectl turns the argument lists renderKubectl produces into one API call.
func parseKubectl(args []string, stdin string) (kubeCall, bool, error) {
	var rest []string
	dryRun := false
	for _, a := range args {
		if a == "--dry-run=server" {
			dryRun = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) == 0 {
		return kubeCall{}, false, errors.New("no kubectl command")
	}
	flag := func(name string) string {
		for i := 1; i+1 < len(rest); i++ {
			if rest[i] == name {
				return rest[i+1]
			}
		}
		return ""
	}
	switch rest[0] {
	case "apply":
		if flag("-f") != "-" {
			return kubeCall{}, false, errors.New("apply needs -f -")
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(stdin), &obj); err != nil {
			return kubeCall{}, false, fmt.Errorf("manifest: %w", err)
		}
		kind, _ := obj["kind"].(string)
		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		if obj["apiVersion"] != "gryvia.io/v1alpha1" || name == "" {
			return kubeCall{}, false, errors.New("manifest must be a named gryvia.io/v1alpha1 object")
		}
		p, err := objectPath(kind, ns, name)
		if err != nil {
			return kubeCall{}, false, err
		}
		body, err := json.Marshal(obj)
		if err != nil {
			return kubeCall{}, false, err
		}
		return kubeCall{method: http.MethodPatch, path: p, contentType: "application/apply-patch+yaml", body: body}, dryRun, nil
	case "patch":
		if len(rest) < 3 || flag("--type") != "merge" || flag("-p") == "" {
			return kubeCall{}, false, errors.New("patch needs RESOURCE NAME --type merge -p PATCH")
		}
		p, err := objectPath(kubeResources[rest[1]], flag("-n"), rest[2])
		if err != nil {
			return kubeCall{}, false, err
		}
		return kubeCall{method: http.MethodPatch, path: p, contentType: "application/merge-patch+json", body: []byte(flag("-p"))}, dryRun, nil
	case "delete":
		name, ok := strings.CutPrefix(flag("--field-selector"), "metadata.name=")
		if len(rest) < 2 || !ok || name == "" || flag("-l") != "app.kubernetes.io/managed-by=zyntra" {
			return kubeCall{}, false, errors.New("delete needs RESOURCE -l app.kubernetes.io/managed-by=zyntra --field-selector metadata.name=NAME")
		}
		p, err := objectPath(kubeResources[rest[1]], flag("-n"), name)
		if err != nil {
			return kubeCall{}, false, err
		}
		ignore := false
		for _, a := range rest {
			ignore = ignore || a == "--ignore-not-found"
		}
		return kubeCall{method: http.MethodDelete, path: p, managedOnly: true, ignoreNotFound: ignore}, dryRun, nil
	}
	return kubeCall{}, false, fmt.Errorf("the in-cluster runner does not run kubectl %s", rest[0])
}

type kube struct {
	cli   *http.Client
	base  string
	token string
}

func (k *kube) do(ctx context.Context, method, path string, q url.Values, contentType string, body []byte) (int, []byte, error) {
	u := k.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := k.cli.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("kubernetes API: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

func apiError(code int, b []byte) error {
	var st struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &st)
	if st.Message == "" {
		st.Message = http.StatusText(code)
	}
	if code == http.StatusForbidden {
		st.Message += " (the chart grants Zyntra's actions with kubernetes.actions=true)"
	}
	return fmt.Errorf("kubernetes API: %s", st.Message)
}

func (k *kube) run(ctx context.Context, c kubeCall, dryRun bool) (string, error) {
	q := url.Values{}
	if dryRun {
		q.Set("dryRun", "All")
	}
	suffix := ""
	if dryRun {
		suffix = " (server dry run)"
	}
	if c.managedOnly {
		code, b, err := k.do(ctx, http.MethodGet, c.path, nil, "", nil)
		if err != nil {
			return "", err
		}
		if code == http.StatusNotFound && c.ignoreNotFound {
			return "not found; nothing to delete" + suffix, nil
		}
		if code != http.StatusOK {
			return "", apiError(code, b)
		}
		var obj struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(b, &obj)
		if obj.Metadata.Labels["app.kubernetes.io/managed-by"] != "zyntra" {
			return "not managed by zyntra; left in place" + suffix, nil
		}
	}
	if c.contentType == "application/apply-patch+yaml" {
		q.Set("fieldManager", fieldManager)
		q.Set("force", "true")
	}
	code, b, err := k.do(ctx, c.method, c.path, q, c.contentType, c.body)
	if err != nil {
		return "", err
	}
	if code == http.StatusNotFound && c.ignoreNotFound {
		return "not found; nothing to delete" + suffix, nil
	}
	if code < 200 || code > 299 {
		return "", apiError(code, b)
	}
	var obj struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(b, &obj)
	verb := map[string]string{http.MethodPatch: "applied", http.MethodDelete: "deleted"}[c.method]
	if c.contentType == "application/merge-patch+json" {
		verb = "patched"
	}
	name := obj.Metadata.Name
	if name == "" {
		name = c.path[strings.LastIndex(c.path, "/")+1:]
	}
	return fmt.Sprintf("%s %s%s", strings.ToLower(obj.Kind+" "+name), verb, suffix), nil
}
