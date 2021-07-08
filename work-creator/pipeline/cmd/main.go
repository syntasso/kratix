package main

import (
	"flag"
	"fmt"
	"os"

	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
	"github.com/syntasso/synpl-platform/work-creator/pipeline"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

func main() {
	var inputDirectoy string
	var identifier string
	flag.StringVar(&inputDirectoy, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	flag.StringVar(&identifier, "identifier", "", "Unique name of the Work resource to be created")
	flag.Parse()

	if identifier == "" {
		fmt.Println("Must provide -identifier")
		os.Exit(1)
	}

	if inputDirectoy == "" {
		fmt.Println("Must provide -input-directory")
		os.Exit(1)
	}

	//Teach our client to speak platformv1alpha1.Work
	platformv1alpha1.AddToScheme(scheme.Scheme)

	k8sClient, err := getClient()
	if err != nil {
		fmt.Println("Error creating k8s client")
		os.Exit(1)
	}

	workCreator := pipeline.WorkCreator{
		K8sClient: k8sClient,
	}
	err = workCreator.Execute(inputDirectoy, identifier)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func getClient() (client.Client, error) {
	config := ctrl.GetConfigOrDie()
	options := cluster.Options{}
	options = setOptionsDefaults(options)

	// Create the mapper provider
	mapper, err := options.MapperProvider(config)
	if err != nil {
		options.Logger.Error(err, "Failed to get API Group-Resources")
		return nil, err
	}

	// Create the cache for the cached read client and registering informers
	cache, err := options.NewCache(config, cache.Options{Scheme: options.Scheme, Mapper: mapper, Resync: options.SyncPeriod, Namespace: options.Namespace})
	if err != nil {
		return nil, err
	}

	clientOptions := client.Options{Scheme: options.Scheme, Mapper: mapper}

	return options.ClientBuilder.
		Build(cache, config, clientOptions)
}

// setOptionsDefaults set default values for Options fields
func setOptionsDefaults(options cluster.Options) cluster.Options {
	// Use the Kubernetes client-go scheme if none is specified
	if options.Scheme == nil {
		options.Scheme = scheme.Scheme
	}

	if options.MapperProvider == nil {
		options.MapperProvider = func(c *rest.Config) (meta.RESTMapper, error) {
			return apiutil.NewDynamicRESTMapper(c)
		}
	}

	// Allow the client builder to be mocked
	if options.ClientBuilder == nil {
		options.ClientBuilder = cluster.NewClientBuilder()
	}

	// Allow newCache to be mocked
	if options.NewCache == nil {
		options.NewCache = cache.New
	}

	return options
}
