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

    sourceSets {
        getByName("main") {
            jniLibs.srcDirs("libs/jni")
        }
    }
}

dependencies {
    implementation(files("libs/echproxy.aar"))
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")
}
