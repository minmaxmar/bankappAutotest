package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type SwaggerSpec struct {
	Paths map[string]interface{} `json:"paths"`
}

type Parameter struct {
	In       string `json:"in"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     string `json:"type"`
}

type MethodInfo struct {
	Route      string
	Method     string
	Parameters []Parameter
	Responses  map[string]interface{}
	Summary    string
}

type RouteMethods map[string]MethodInfo

type RouteMethodsList []MethodInfo

const maxConcurrency = 5

func fetchSwaggerSpec(url string) (*SwaggerSpec, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var spec SwaggerSpec
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func parsePaths(rawPaths map[string]interface{}) RouteMethodsList {
	routes := map[string]RouteMethods{}

	methods := RouteMethodsList{}

	for route, methodsRaw := range rawPaths {
		methodsMap, ok := methodsRaw.(map[string]interface{})
		if !ok {
			continue
		}
		routeMethods := RouteMethods{}

		for methodName, methodRaw := range methodsMap {
			methodData, ok := methodRaw.(map[string]interface{})
			if !ok {
				continue
			}

			var params []Parameter
			if rawParams, exists := methodData["parameters"]; exists {
				if paramList, ok := rawParams.([]interface{}); ok {
					for _, p := range paramList {
						pMap, ok := p.(map[string]interface{})
						if !ok {
							continue
						}
						param := Parameter{
							In:       getString(pMap, "in"),
							Name:     getString(pMap, "name"),
							Required: getBool(pMap, "required"),
							Type:     getString(pMap, "type"),
						}
						params = append(params, param)
					}
				}
			}

			responses := map[string]interface{}{}
			if respRaw, exists := methodData["responses"]; exists {
				responses, _ = respRaw.(map[string]interface{})
			}
			summary := getString(methodData, "summary")

			methodInfo := MethodInfo{
				Route:      route,
				Method:     methodName,
				Parameters: params,
				Responses:  responses,
				Summary:    summary,
			}

			methods = append(methods, methodInfo)

			// routeMethods[strings.ToLower(methodName)] = methodInfo
		}
		routes[route] = routeMethods
	}
	return methods
}

func debugMethod(info MethodInfo) {
	fmt.Printf("Route: %s\n", info.Route)
	fmt.Printf("  Method: %s\n", info.Method)
	fmt.Printf("    Summary: %s\n", info.Summary)
	fmt.Printf("    Parameters:\n")
	for _, p := range info.Parameters {
		fmt.Printf("      - %s (%s), required: %v\n", p.Name, p.In, p.Required)
	}
	fmt.Printf("    Responses:\n")
	for code, resp := range info.Responses {
		fmt.Printf("      %s: %v\n", code, resp)
	}

}

func fanoutGoroutine(ctx context.Context) chan error {
	ticker := time.NewTicker(1 * time.Minute)
	errChan := make(chan error)
	go func() {
		defer ticker.Stop()
		defer close(errChan)
		for {
			select {
			case <-ticker.C:
				fmt.Println("deliver processed requests to postgres")
			case <-ctx.Done():
				fmt.Println("Shutting down ProduceDeleteExpiredCards goroutine.")
				return
			}
		}
	}()

	return errChan
}

func main() {

	// TODO: map + mutex to update common map from parsePaths
	swaggerURL := "http://localhost:8080/swagger.json"

	spec, err := fetchSwaggerSpec(swaggerURL)
	if err != nil {
		fmt.Printf("Error fetching Swagger spec: %v\n", err)
		return
	}

	parsedSpec := parsePaths(spec.Paths)
	methodsCh := make(chan MethodInfo)

	semaphore := make(chan struct{}, maxConcurrency)
	// var wg sync.WaitGroup

	go func() {
		for _, methodInf := range parsedSpec {

			// wg.Add(1)
			go func(m MethodInfo) {
				// !!!! внутри горутины semaphore <- struct{}{}
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				// defer wg.Done()
				// debugMethod(m)
				methodsCh <- m
			}(methodInf)
		}
		// for i := 0; i < maxConcurrency; i++ {
		// 	semaphore <- struct{}{}
		// }
		// wg.Wait()
		close(methodsCh)
	}()

	// TODO: FAN-OUT goroutine to deliver processed requests to postgres
	for method := range methodsCh {
		fmt.Println("Processed method:", method)
	}
}
