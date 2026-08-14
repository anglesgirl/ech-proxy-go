plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.anglesgirl.xprobe"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.anglesgirl.xprobe"
        minSdk = 27
        targetSdk = 34
        versionCode = 1
        versionName = "1.0"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }
}

dependencies {
    implementation(fileTree("libs") { include("*.aar") })
    implementation("androidx.core:core-ktx:1.12.0")
    implementation("androidx.appcompat:appcompat:1.6.1")
    implementation("androidx.activity:activity-ktx:1.8.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.7.3")
}
