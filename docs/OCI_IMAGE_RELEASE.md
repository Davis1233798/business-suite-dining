# OCI image release

Publish native AMD64 and ARM64 images for each main revision. Preserve
the existing AMD64 tag and use an ARM64 suffix for the OCI host. The
readiness response includes the build revision for both architectures.
The image workflow owns validation and publication. Private runtime
provisioning and the public route are subsequent deployment steps.

Validate the health revision in Go and in both native container builds.

Pull requests validate both architectures. Pushes to main validate and publish; feature branch pushes do not start an additional matrix.
