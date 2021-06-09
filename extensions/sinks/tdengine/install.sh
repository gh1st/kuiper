#!/bin/sh
set -e -x -u

if [ -z "$1" ]
then
    echo "version is empty."
	exit 5
fi

url="https://www.taosdata.com/download/download-all.php?pkgType=tdengine_linux&pkgName=TDengine-client-$1-Linux-x64.tar.gz"
if [ "$(uname -m)" = "aarch64" ]; then \
	url="https://www.taosdata.com/download/download-all.php?pkgType=tdengine_linux&pkgName=TDengine-client-$1-Linux-aarch64.tar.gz"
fi
zip="TDengine-client.tar.gz"
wget -T 280 -O "$zip" "$url"

if ! [ -e $zip ]
then
	echo "Not downloaded to the installation package."
	exit 2
fi

dir="TDengine-client"
mkdir "$dir"
tar -xzvf "$zip" -C ./"$dir" --strip-components 1
rm "$zip"

if ! [ -e $dir ]
then
	echo "Failed to decompress Taos client."
	exit 3
fi

cd "$dir"
ret=""
for file in ./*
do
	if [ -x $file -a ! -d $file ]
	then
		./"$file"
		ret="successful"
	fi
done

cd ../
rm -rf "$dir"

if [ -z "$ret" ]
then
    echo "not found script."
	exit 4
fi
