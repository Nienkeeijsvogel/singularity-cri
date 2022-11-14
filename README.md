 


Singularity-CRI consists of two separate services: runtime and image, each of which implements 
K8s RuntimeService and ImageService respectively.

## Quick start

Complete documentation can be found [here](https://sylabs.io/guides/cri/1.0/user-guide). 
Further a quick steps provided to set up Singularity-CRI from source.

For Ubuntu 22.04 the /etc/default/grub file should be replaced by https://github.com/Nienkeeijsvogel/sykube/blob/master/grub after which sudo update-grub and sudo init 6 should be run.
On ubuntu 20.04,18.04,centos 8,9 the next steps work without any system changes.
In order to use Singularity-CRI install the following:


- [git](https://git-scm.com/downloads)
- [go 1.16](https://golang.org/doc/install)
- [Singularity 3.5.2 with OCI support](https://github.com/sylabs/singularity/blob/master/INSTALL.md)
- [inotify](http://man7.org/linux/man-pages/man7/inotify.7.html) for device plugin
- socat package to perform port forwarding

Since Singularity-CRI is now built with [go modules](https://github.com/golang/go/wiki/Modules)
there is no need to create standard [go workspace](https://golang.org/doc/code.html). If you still
prefer keeping source code under GOPATH make sure GO111MODULE is set. 

The following assumes you are installing Singularity-CRI from source outside GOPATH:
```bash
git clone https://github.com/Nienkeeijsvogel/singularity-cri && \
cd singularity-cri && \
git checkout v1.0.0-beta.8 && \
make && sudo make install  
```

This will build the _sycri_ binary with CRI implementation. After installation you will find it in `/usr/local/bin`.

Singularity-CRI works with Singularity runtime directly so you need to have
`/usr/local/libexec/singularity/bin` your PATH environment variable.

To start Singularity-CRI there are two options:

simply run _sycri_ binary. By default it listens for requests on
`unix:///var/run/singularity.sock` and stores image files at `/var/lib/singularity`. 
This behaviour may be configured with config file, run `sycri -h` for more details.

Create a systemd service file.

## Contributing

Community contributions are always greatly appreciated. To start developing Singularity-CRI,
check out the [guidelines for contributing](CONTRIBUTING.md).

We also welcome contributions to our [user docs](https://github.com/sylabs/singularity-cri-userdocs).

## Support

To get help with Singularity-CRI, check out the [community Portal](https://sylabs.io/resources/community).
Also feel free to raise issues here or contact [maintainers](CONTRIBUTORS.md).

For additional support, [contact us](https://sylabs.io/contact-us) to receive more information.

## License

_Unless otherwise noted, this project is licensed under a Apache 2 license found in the [license file](LICENSE)._
