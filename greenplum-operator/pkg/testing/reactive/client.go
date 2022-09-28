package reactive

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type Client struct {
	testing.Fake
	delegate     client.Client
	clientScheme *runtime.Scheme
	restMapper   meta.RESTMapper
}

var _ client.Client = &Client{}

func NewClient(delegate client.Client, clientScheme *runtime.Scheme) *Client {
	gvs := clientScheme.PrioritizedVersionsAllGroups()
	restMapper := meta.NewDefaultRESTMapper(gvs)
	knownTypes := clientScheme.AllKnownTypes()
	for gvk := range knownTypes {
		restMapper.Add(gvk, meta.RESTScopeNamespace)
	}

	r := &Client{
		delegate:     delegate,
		clientScheme: clientScheme,
		restMapper:   restMapper,
	}

	r.PrependReactor("*", "*", func(action testing.Action) (bool, runtime.Object, error) {
		ctx := context.TODO()
		// TODO: switch on action.GetVerb() instead.
		switch a := action.(type) {
		case testing.GetActionImpl:
			key := types.NamespacedName{
				Name:      a.GetName(),
				Namespace: a.GetNamespace(),
			}
			obj := r.newNamedObject(r.kindForResource(a.GetResource()), a.GetNamespace(), a.GetName())
			err := r.delegate.Get(ctx, key, obj)
			return true, obj, err
		case testing.CreateActionImpl:
			err := r.delegate.Create(ctx, a.GetObject())
			return true, nil, err
		case testing.DeleteActionImpl:
			obj := r.newNamedObject(r.kindForResource(a.GetResource()), a.GetNamespace(), a.GetName())
			err := r.delegate.Delete(ctx, obj)
			return true, nil, err
		case testing.UpdateActionImpl:
			err := r.delegate.Update(ctx, a.GetObject())
			return true, nil, err
		case testing.PatchActionImpl:
			obj := r.newNamedObject(r.kindForResource(a.GetResource()), a.GetNamespace(), a.GetName())
			patch := client.ConstantPatch(a.GetPatchType(), a.GetPatch())
			err := r.delegate.Patch(ctx, obj, patch)
			return true, nil, err
		case testing.ListActionImpl:
			obj := r.newObject(a.GetKind())
			err := r.delegate.List(ctx, obj,
				client.MatchingFieldsSelector{Selector: a.GetListRestrictions().Fields},
				client.MatchingLabelsSelector{Selector: a.GetListRestrictions().Labels},
				client.InNamespace(a.GetNamespace()),
			)
			return true, obj, err
		default:
			return true, nil, fmt.Errorf("unsupported action for verb %#v", action.GetVerb())
		}
	})

	return r
}

func (r *Client) gvrForObject(obj runtime.Object) schema.GroupVersionResource {
	defer GinkgoRecover()
	kinds, _, err := r.clientScheme.ObjectKinds(obj)
	Expect(err).NotTo(HaveOccurred())
	Expect(kinds).To(HaveLen(1))
	gvk := kinds[0]

	rm, err := r.restMapper.RESTMapping(gvk.GroupKind())
	Expect(err).NotTo(HaveOccurred())
	gvr := rm.Resource

	return gvr
}

func (r *Client) kindForResource(resource schema.GroupVersionResource) schema.GroupVersionKind {
	defer GinkgoRecover()
	kind, err := r.restMapper.KindFor(resource)
	Expect(err).NotTo(HaveOccurred())
	return kind
}

func (r *Client) newNamedObject(kind schema.GroupVersionKind, namespace, name string) runtime.Object {
	defer GinkgoRecover()
	obj := r.newObject(kind)
	mo, err := meta.Accessor(obj)
	Expect(err).NotTo(HaveOccurred())
	mo.SetNamespace(namespace)
	mo.SetName(name)
	return obj
}

func (r *Client) newObject(kind schema.GroupVersionKind) runtime.Object {
	defer GinkgoRecover()
	obj, err := r.clientScheme.New(kind)
	Expect(err).NotTo(HaveOccurred())
	return obj
}

func (r *Client) populateGVK(obj runtime.Object) {
	defer GinkgoRecover()
	// Set GVK using reflection. Normally the apiserver would populate this, but we need it earlier.
	gvk, err := apiutil.GVKForObject(obj, r.clientScheme)
	Expect(err).NotTo(HaveOccurred())
	obj.GetObjectKind().SetGroupVersionKind(gvk)
}

func (r *Client) Get(ctx context.Context, key client.ObjectKey, obj runtime.Object) error {
	action := testing.NewGetAction(r.gvrForObject(obj), key.Namespace, key.Name)
	retrievedObj, err := r.Invokes(action, nil)
	if err != nil {
		return err
	}
	return r.clientScheme.Convert(retrievedObj, obj, nil)
}

func (r *Client) List(ctx context.Context, list runtime.Object, opts ...client.ListOption) error {
	defer GinkgoRecover()

	listOpts := client.ListOptions{}
	listOpts.ApplyOptions(opts)

	listGvk, err := apiutil.GVKForObject(list, r.clientScheme)
	if err != nil {
		return err
	}

	if !strings.HasSuffix(listGvk.Kind, "List") {
		return fmt.Errorf("non-list type %T (kind %q) passed as output", list, listGvk)
	}
	// we need the non-list GVK, so chop off the "List" from the end of the kind
	gvk := listGvk
	gvk.Kind = gvk.Kind[:len(gvk.Kind)-len("List")]

	gvr, _ := meta.UnsafeGuessKindToResource(gvk)

	action := testing.NewListAction(gvr, listGvk, listOpts.Namespace, *listOpts.AsListOptions())
	retrievedObj, err := r.Invokes(action, nil)
	if err != nil {
		return err
	}
	return r.clientScheme.Convert(retrievedObj, list, nil)
}

func (r *Client) Create(ctx context.Context, obj runtime.Object, opts ...client.CreateOption) error {
	defer GinkgoRecover()
	Expect(opts).To(BeEmpty(), "we can't handle opts")
	object, err := meta.Accessor(obj)
	if err != nil {
		return errors.Wrap(err, "failed creating object")
	}

	r.populateGVK(obj)

	action := testing.NewCreateAction(r.gvrForObject(obj), object.GetNamespace(), obj)
	_, err = r.Invokes(action, nil)
	return err
}

func (r *Client) Delete(ctx context.Context, obj runtime.Object, opts ...client.DeleteOption) error {
	defer GinkgoRecover()

	// TODO: We are just dropping these options on the floor... this is the same thing
	//       that the controller-runtime fake client does, so it doesn't seem too unusual
	//       but is that really the right thing to do here?
	deleteOpts := client.DeleteOptions{}
	deleteOpts.ApplyOptions(opts)

	object, err := meta.Accessor(obj)
	if err != nil {
		return errors.Wrap(err, "failed deleting object")
	}

	action := testing.NewDeleteAction(r.gvrForObject(obj), object.GetNamespace(), object.GetName())
	_, err = r.Invokes(action, nil)
	return err
}

func (r *Client) DeleteAllOf(ctx context.Context, obj runtime.Object, opts ...client.DeleteAllOfOption) error {
	panic("implement me")
}

func (r *Client) Update(ctx context.Context, obj runtime.Object, opts ...client.UpdateOption) error {
	defer GinkgoRecover()
	Expect(opts).To(BeEmpty(), "we can't handle opts")
	object, err := meta.Accessor(obj)
	if err != nil {
		return errors.Wrap(err, "failed updating object")
	}

	r.populateGVK(obj)

	action := testing.NewUpdateAction(r.gvrForObject(obj), object.GetNamespace(), obj)
	_, err = r.Invokes(action, nil)
	return err
}

func (r *Client) Patch(ctx context.Context, obj runtime.Object, patch client.Patch, opts ...client.PatchOption) error {
	defer GinkgoRecover()
	Expect(opts).To(BeEmpty(), "we can't handle opts")
	object, err := meta.Accessor(obj)
	if err != nil {
		return errors.Wrap(err, "failed patching object")
	}
	p, err := patch.Data(obj)
	if err != nil {
		return errors.Wrap(err, "failed patching object")
	}
	action := testing.NewPatchAction(r.gvrForObject(obj), object.GetNamespace(), object.GetName(), patch.Type(), p)
	_, err = r.Invokes(action, nil)
	return err
}

func (r *Client) Status() client.StatusWriter {
	return r
}
