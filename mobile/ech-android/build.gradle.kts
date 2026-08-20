plugins {
    id("com.android.library")
    kotlin("android")
}

android {
    namespace = "com.anglesgirl.ech"
    compileSdk = 34

    defaultConfig {
        minSdk = 24
        consumerProguardFiles("consumer-rules.pro")
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    // Go binding 在 echproxy.aar 中随 SDK 一起交付；本模块只做编译期引用。
    compileOnly(files("libs/echproxy-classes.jar"))
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")
}
