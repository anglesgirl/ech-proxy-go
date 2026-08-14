plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.anglesgirl.echbrowser"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.anglesgirl.echbrowser"
        minSdk = 27
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
        // 仅 arm64-v8a：手机真机分发，APK 体积最小化
        ndk { abiFilters += listOf("arm64-v8a") }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        viewBinding = true
    }
}

dependencies {
    // GeckoView（Firefox 内核）—— 最新版
    implementation("org.mozilla.geckoview:geckoview:153.0.20260803132010")
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.appcompat:appcompat:1.7.0")

    // 本地 DoH 注入服务器（gomobile 绑定）
    implementation(fileTree("libs") { include("*.aar") })
}
