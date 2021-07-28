// SPDX-License-Identifier: AGPL-3.0-only
package mutate

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	v1beta1 "k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// registry/namespace/image:tag
type DockerImageUrl struct {
	registry string
	namespace string
	image string
	tag string
}

// takes toto:tata or toto, gives tata or latest
func getImgTag(img string) string {
	imgTagArr := strings.Split(img, ":")
	tag := "latest"
	if len(imgTagArr) == 2 {
		tag = imgTagArr[1]
	}

		return tag
}

// takes toto:tata or toto, gives toto
func getImgName(img string) string {
		imgTagArr := strings.Split(img, ":")
		return imgTagArr[0]
}

func getDockerImageUrl(img string) DockerImageUrl {
	imgArr := strings.Split(img, "/")
	// Not prefixed with a site
	if len(imgArr) == 1 {
		// Case busybox or busybox:tag
		return DockerImageUrl{
			registry: "docker.io", // default
			namespace: "library", // default
			image: getImgName(img),
			tag: getImgTag(img),
		}
	}

	imgUrl := imgArr[0]
	// Case docker.io/busybox
	if len(imgArr) == 2 && imgUrl == "docker.io" {
		return DockerImageUrl {
			registry: imgUrl,
			namespace: "library",
			image: getImgName(imgArr[1]),
			tag: getImgTag(imgArr[1]),
		}
	}

	// Case toto/tata (and ! gcr.io/toto)
	if len(imgArr) == 2 && !strings.Contains(imgUrl, ".") {
		return DockerImageUrl {
			registry: "docker.io",
			namespace: imgArr[0],
			image: getImgName(imgArr[1]),
			tag: getImgTag(imgArr[1]),
		}
	}

	if len(imgArr) == 2 && strings.Contains(imgUrl, ".") {
	return DockerImageUrl {
			registry: imgUrl,
			namespace: "", // ??? TODO does it exist?
			image: getImgName(imgArr[1]),
			tag: getImgTag(imgArr[1]),
		}
	}

	// case toto.io/tata/titi[:tag]
	return DockerImageUrl {
		registry: imgArr[0],
		namespace: imgArr[1],
		image: getImgName(imgArr[2]),
		tag: getImgTag(imgArr[2]),
	}
}

func (i DockerImageUrl) String() string {
	if i.namespace == "" {
		return fmt.Sprintf("%s/%s:%s",
			i.registry,
			i.image,
			i.tag)
	}
	return fmt.Sprintf("%s/%s/%s:%s",
		i.registry,
		i.namespace,
		i.image,
		i.tag)
}

func GetPatchedImageUrl(img, registry string) string {
	patchimg := getDockerImageUrl(img)

	if patchimg.registry == "docker.io" &&
		patchimg.image != "registry" {
		patchimg.registry = registry
	}

	return patchimg.String()
}

func getPatchFromContainerList(ctn []corev1.Container, registry, containerType string) []map[string]string {
	patchList := []map[string]string{}
	for i := range ctn {
		img := ctn[i].Image

		patchedImg := GetPatchedImageUrl(img, registry)

		// In case there's a tag
		if strings.HasPrefix(patchedImg, "docker.io/library/registry") ||
		   strings.HasPrefix(patchedImg, "registry") {
			// We don't patch the registry to avoid the bootstrap problem
			continue
		}

		// No need to patch if it's the same
		if img == patchedImg {
			continue
		}

		patch := map[string]string{
			"op":    "replace",
			"path":  fmt.Sprintf("/spec/%s/%d/image", containerType, i),
			"value": patchedImg,
		}
		patchList = append(patchList, patch)
	}

	return patchList
}

func Mutate(body []byte, verbose bool, registry string) ([]byte, error) {
	if verbose {
		log.Printf("recv: %s\n", string(body))
	}

	admReview := v1beta1.AdmissionReview{}
	if err := json.Unmarshal(body, &admReview); err != nil {
		return nil, fmt.Errorf("Unmarshaling request error %s", err)
	}

	var err error
	var pod *corev1.Pod

	responseBody := []byte{}
	ar := admReview.Request
	resp := v1beta1.AdmissionResponse{}

	if ar == nil {
		if verbose {
			log.Printf("resp: %s\n", string(responseBody))
		}

		return responseBody, nil
	}

	if err := json.Unmarshal(ar.Object.Raw, &pod); err != nil {
		log.Println("FATAL Error ", err)
		return nil, fmt.Errorf("Unmarshal pod json error %v", err)
	}

	resp.Allowed = true
	resp.UID = ar.UID
	pT := v1beta1.PatchTypeJSONPatch
	resp.PatchType = &pT

	resp.AuditAnnotations = map[string]string{
		"k8s-proxy-image-swapper": "mutated",
	}

	patchList := getPatchFromContainerList(pod.Spec.Containers, registry, "containers")
	patchList = append(patchList, getPatchFromContainerList(pod.Spec.InitContainers, registry, "initContainers")...)
	resp.Patch, err = json.Marshal(patchList)

	// We cannot fail
	resp.Result = &metav1.Status{
		Status: "Success",
	}

	admReview.Response = &resp
	responseBody, err = json.Marshal(admReview)
	if err != nil {
		log.Println("FATAL Error ", err)
		return nil, err
	}
	return responseBody, nil
}
