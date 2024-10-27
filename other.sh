#!/bin/bash

# 定义变量
REPO_URL="https://github.com/MetaCubeX/meta-rules-dat"
BRANCH="meta"
TARGET_DIR="geo/geosite"
DEST_DIR="processed-files" # 替换为实际路径
mkdir -p processed-files
# 克隆特定文件夹
git clone --depth 1 --branch $BRANCH $REPO_URL temp_repo
cd temp_repo/$TARGET_DIR || exit

# 保留扩展名为 .list 的文件，删除文件内的 "+" 字符
for file in *.list; do
    sed 's/+//g' "$file" > "$DEST_DIR/$file"
done

