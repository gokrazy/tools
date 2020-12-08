# gokr-packer

## Default use case: directly writing to an SD card

See [gokrazy](https://github.com/gokrazy/gokrazy)

## Alternative: Creating file system images

Creating individual file system images allows to conveniently archive
the images and easily roll back to a previous state.

Specify any combination of `-overwrite_root` and `-overwrite_boot`
flags, e.g.:

```
gokr-packer \
  -overwrite_boot=/tmp/boot.fat \
  -overwrite_root=/tmp/root.fat \
  github.com/gokrazy/hello
```

To overwrite the SD card `/dev/sdx`’s partitions with these images,
use:

```
sudo dd if=/tmp/boot.fat of=/dev/sdx1 bs=1M
sudo dd if=/tmp/root.fat of=/dev/sdx2 bs=1M
```

If you’re curious, you can also loop-mount the file system images on
your machine to inspect them:

```
sudo mkdir /mnt/loop
sudo mount -o loop -t vfat /tmp/boot.fat /mnt/loop
ls -lR /mnt/loop
sudo umount /mnt/loop
```

## Alternative: Creating an SD card image

If you prefer, you can also create a full SD card image. It will be
slower to transfer to an SD card, but the 1:1 mapping of a file to an
SD card state might be appealing.

```
gokr-packer \
  -overwrite=/tmp/full.img \
  -target_storage_bytes=2147483648 \
  github.com/gokrazy/hello
```

To overwrite the SD card `/dev/sdx` with the image, use:

```
sudo dd if=/tmp/full.img of=/dev/sdx bs=1M
```

If you’re curious, you can also loop-mount the file systems of the
image on your machine to inspect them:

```
sudo kpartx -avs /tmp/full.img
sudo mkdir /mnt/{boot,root}
sudo mount -t vfat /dev/mapper/loop0p1 /mnt/boot
sudo mount /dev/mapper/loop0p2 /mnt/root
ls -lR /mnt/{boot,root}
sudo umount /mnt/{boot,root}
sudo kpartx -d /tmp/full.img
```
