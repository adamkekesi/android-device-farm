// Command farmctl is a small client for the Android Device Farm: it acquires,
// heartbeats, and releases DeviceLeases against the cluster API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	name := fs.String("name", "", "lease name (acquire: optional, a name is generated)")
	class := fs.String("class", "", "device class to lease")
	requester := fs.String("requester", defaultRequester(), "opaque requester id")
	ttl := fs.Int("ttl", 300, "lease TTL seconds")
	waitBound := fs.Bool("wait", true, "acquire: wait until Bound")
	timeout := fs.Duration("timeout", 5*time.Minute, "acquire: max wait")
	_ = fs.Parse(os.Args[2:])

	c := newClient()
	ctx := context.Background()

	switch cmd {
	case "acquire":
		mustClass(*class)
		acquire(ctx, c, *ns, *name, *class, *requester, int32(*ttl), *waitBound, *timeout)
	case "heartbeat":
		mustName(*name)
		heartbeat(ctx, c, *ns, *name)
	case "release":
		mustName(*name)
		release(ctx, c, *ns, *name)
	default:
		usage()
	}
}

func acquire(ctx context.Context, c client.Client, ns, name, class, requester string, ttl int32, waitBound bool, timeout time.Duration) {
	lease := &farmv1alpha1.DeviceLease{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec: farmv1alpha1.DeviceLeaseSpec{
			ClassRef: class, Requester: requester, TTLSeconds: ttl,
		},
	}
	if name != "" {
		lease.Name = name
	} else {
		lease.GenerateName = "lease-"
	}
	if err := c.Create(ctx, lease); err != nil {
		fatal("create lease: %v", err)
	}
	fmt.Printf("created lease %s/%s\n", lease.Namespace, lease.Name)

	if !waitBound {
		return
	}
	key := client.ObjectKeyFromObject(lease)
	err := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, key, lease); err != nil {
			return false, err
		}
		switch lease.Status.Phase {
		case farmv1alpha1.LeaseBound:
			return true, nil
		case farmv1alpha1.LeaseFailed:
			return false, fmt.Errorf("lease failed")
		default:
			return false, nil
		}
	})
	if err != nil {
		fatal("waiting for bind: %v", err)
	}
	fmt.Printf("bound: device=%s adb=%s\n", lease.Status.DeviceRef, lease.Status.ADBEndpoint)
}

func heartbeat(ctx context.Context, c client.Client, ns, name string) {
	var lease farmv1alpha1.DeviceLease
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, &lease); err != nil {
		fatal("get lease: %v", err)
	}
	patched := lease.DeepCopy()
	now := metav1.Now()
	patched.Status.LastHeartbeat = &now
	if err := c.Status().Patch(ctx, patched, client.MergeFrom(&lease)); err != nil {
		fatal("heartbeat: %v", err)
	}
	fmt.Printf("heartbeat %s/%s at %s\n", ns, name, now.Format(time.RFC3339))
}

func release(ctx context.Context, c client.Client, ns, name string) {
	lease := &farmv1alpha1.DeviceLease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := c.Delete(ctx, lease); err != nil && !apierrors.IsNotFound(err) {
		fatal("release: %v", err)
	}
	fmt.Printf("released %s/%s\n", ns, name)
}

func newClient() client.Client {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(farmv1alpha1.AddToScheme(scheme))
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fatal("kubeconfig: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fatal("client: %v", err)
	}
	return c
}

func defaultRequester() string {
	if v := os.Getenv("FARM_REQUESTER"); v != "" {
		return v
	}
	host, _ := os.Hostname()
	return host
}

func mustClass(class string) {
	if class == "" {
		fatal("--class is required")
	}
}

func mustName(name string) {
	if name == "" {
		fatal("--name is required")
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `farmctl — Android Device Farm lease client

Usage:
  farmctl acquire   --class <name> [--namespace ns] [--name n] [--ttl 300] [--wait] [--timeout 5m]
  farmctl heartbeat --name <lease> [--namespace ns]
  farmctl release   --name <lease> [--namespace ns]

Tip for CI: acquire with --wait, then run a pinger that calls `+"`farmctl heartbeat`"+`
on an interval shorter than the TTL, and `+"`farmctl release`"+` when the job ends.
`)
	os.Exit(2)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
